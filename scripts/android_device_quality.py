"""Trusted exact-release-APK business checks on an operator-bound USB device."""
from datetime import datetime, timezone
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import socket
import ssl
import stat
import tarfile
import tempfile
import time
import traceback
import uuid
from urllib.parse import urlsplit
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET

from android_quality_common import (
    sha, validate_apk, source_snapshot, device_lock, package_path,
    require_absent_package, installed_apk_digest, prepare_install_apk,
    validate_signature, command,
)


def api_origin(value):
    if not isinstance(value, str) or any(c.isspace() or ord(c) < 32 for c in value):
        raise ValueError('explicit HTTPS test-API origin required')
    parsed = urlsplit(value)
    try:
        port = parsed.port
    except ValueError as error:
        raise ValueError('invalid HTTPS port') from error
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.path not in {'', '/'}
            or '?' in value or '#' in value or port is not None and not 1 <= port <= 65535):
        raise ValueError('dedicated test API requires an HTTPS origin without credentials, path, query or fragment')
    return value.rstrip('/')


def unique_json_pairs(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON field')
        result[key] = value
    return result


def load_profile(path):
    path = Path(path)
    if path.is_symlink():
        raise ValueError('profile must be a private regular file')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if (not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600
                or info.st_uid != os.getuid() or info.st_nlink != 1 or info.st_size > 16384):
            raise ValueError('profile must be an owned0600 single-link regular file below16KiB')
        with os.fdopen(fd, 'r', closefd=False) as stream:
            profile = json.load(stream, object_pairs_hook=unique_json_pairs)
    finally:
        os.close(fd)
    required = {'schemaVersion', 'serial', 'adbServerPort', 'manufacturer', 'model', 'fingerprint', 'sdk',
                'package', 'versionCode', 'versionName', 'certificateSHA256', 'mode', 'apiURL', 'dedicatedTestAPI'}
    if not isinstance(profile, dict) or not required <= profile.keys() or profile.keys() - required - {'slotId', 'apiCAFile'}:
        raise ValueError('profile fields are missing or unsupported')
    if type(profile['schemaVersion']) is not int or profile['schemaVersion'] != 1 or profile['dedicatedTestAPI'] is not True:
        raise ValueError('version1 and an explicitly dedicated test API are required')
    for name, low, high in [('adbServerPort', 1024, 65535), ('sdk', 24, 99), ('versionCode', 1, 2100000000)]:
        if type(profile[name]) is not int or not low <= profile[name] <= high:
            raise ValueError('invalid profile ' + name)
    for name, limit in [('manufacturer', 128), ('model', 128), ('fingerprint', 512)]:
        value = profile[name]
        if not isinstance(value, str) or not 1 <= len(value) <= limit or any(ord(c) < 32 for c in value):
            raise ValueError('invalid device profile ' + name)
    patterns = {'serial': r'[A-Za-z0-9][A-Za-z0-9_.-]{0,127}',
                'package': r'[A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+',
                'versionName': r'[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}',
                'certificateSHA256': r'[a-f0-9]{64}'}
    for name, pattern in patterns.items():
        if not isinstance(profile[name], str) or not re.fullmatch(pattern, profile[name]):
            raise ValueError('invalid profile ' + name)
    if profile['serial'].lower().startswith('emulator-') or len(profile['package']) > 255:
        raise ValueError('explicit USB serial and application package required')
    if profile['mode'] not in {'feedback', 'booking'}:
        raise ValueError('only feedback and booking blueprints are supported')
    if profile['mode'] == 'booking' and not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', str(profile.get('slotId', ''))):
        raise ValueError('booking requires an explicit dedicated slot')
    if profile['mode'] == 'feedback' and 'slotId' in profile:
        raise ValueError('feedback profile does not use a booking slot')
    profile['apiURL'] = api_origin(profile['apiURL'])
    if 'apiCAFile' in profile:
        ca = Path(profile['apiCAFile'])
        if not ca.is_absolute() or ca.is_symlink() or not ca.is_file():
            raise ValueError('API CA must be an existing absolute regular file')
    return profile


def physical_identity(adb, profile):
    devices = [line.split() for line in adb('devices', '-l').splitlines()]
    selected = [row for row in devices if row and row[0] == profile['serial']]
    if (len(selected) != 1 or len(selected[0]) < 3 or selected[0][1] != 'device'
            or not any(item.startswith('usb:') and len(item) > 4 for item in selected[0][2:])):
        raise ValueError('approved USB device must be uniquely present and already authorized')
    keys = ['ro.product.manufacturer', 'ro.product.model', 'ro.build.fingerprint', 'ro.build.version.sdk',
            'ro.product.cpu.abilist', 'sys.boot_completed', 'ro.kernel.qemu', 'ro.boot.qemu', 'ro.boot.qemu.avd_name']
    props = {name: adb('shell', 'getprop', name).strip() for name in keys}
    if (props['ro.kernel.qemu'] not in {'', '0'} or props['ro.boot.qemu'] not in {'', '0'}
            or props['ro.boot.qemu.avd_name'] or props['sys.boot_completed'] != '1'
            or props['ro.product.manufacturer'] != profile['manufacturer']
            or props['ro.product.model'] != profile['model']
            or props['ro.build.fingerprint'] != profile['fingerprint']
            or props['ro.build.version.sdk'] != str(profile['sdk'])
            or 'arm64-v8a' not in props['ro.product.cpu.abilist'].split(',')):
        raise ValueError('device properties do not match the approved booted physical-device profile')
    return {'transport': 'usb', 'serial': profile['serial'], 'manufacturer': props['ro.product.manufacturer'],
            'model': props['ro.product.model'], 'fingerprint': props['ro.build.fingerprint'], 'sdk': int(props['ro.build.version.sdk']),
            'abis': props['ro.product.cpu.abilist'].split(','), 'hardwareAttestationVerified': False,
            'identityBasis': 'administrator-bound USB listing and Android properties; trusted QA host required'}


def validate_release(badging, signature, profile):
    packages = re.findall(r"^package: name='([^']+)' versionCode='([0-9]+)' versionName='([^']*)'", badging, re.M)
    expected = (profile['package'], str(profile['versionCode']), profile['versionName'])
    activity = profile['package'] + '.MainActivity'
    activities = re.findall(r"^launchable-activity: name='([^']+)'", badging, re.M)
    if (packages != [expected] or activities != [activity]
            or not re.search(r"^native-code: 'arm64-v8a'\s*$", badging, re.M)):
        raise ValueError('APK package, version, arm64 ABI or blueprint activity differs from approval')
    signer = validate_signature(signature, badging, profile['certificateSHA256'])
    return {**signer, 'package': profile['package'], 'versionCode': profile['versionCode'],
            'versionName': profile['versionName'], 'activity': activity, 'enterpriseIdentityVerified': False}


def cleanup_owned(adb, profile, ownership, expected_digest):
    if not ownership.get('initiallyAbsent') or not ownership.get('installAttempted'):
        return {'packageRemoved': False, 'reason': 'no owned installation was attempted'}
    physical_identity(adb, profile)
    if not package_path(adb, profile['package']):
        return {'packageRemoved': False, 'absentConfirmed': True}
    if installed_apk_digest(adb, profile['package']) != expected_digest:
        raise ValueError('changed installed bytes retained; operator reconciliation required')
    if adb('uninstall', profile['package']).strip() != 'Success':
        raise ValueError('owned-package uninstall was not confirmed')
    require_absent_package(adb, profile['package'])
    return {'packageRemoved': True, 'absentConfirmed': True}


def publish_report(diagnostics, output):
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix='.android-device-report-', dir=output.parent)
    os.close(fd)
    try:
        with tarfile.open(temporary, 'w:gz') as archive:
            for path in sorted(Path(diagnostics).iterdir()):
                if path.is_symlink() or not path.is_file():
                    raise ValueError('only flat regular report entries are supported')
                archive.add(path, arcname=path.name, recursive=False)
        with open(temporary, 'rb') as stream:
            os.fsync(stream.fileno())
        os.link(temporary, output)
    finally:
        os.unlink(temporary)


class APIError(RuntimeError):
    def __init__(self, status, payload):
        self.status, self.payload = status, payload
        message = payload.get('error', 'test API failed') if isinstance(payload, dict) else 'test API failed'
        super().__init__(str(message))


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise APIError(code, {'error': 'test API redirect refused'})


class HTTPSAPI:
    def __init__(self, base_url, ca_file=None):
        self.base = api_origin(base_url)
        self.history = []
        context = ssl.create_default_context(cafile=str(ca_file) if ca_file else None)
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
            urllib.request.HTTPSHandler(context=context), NoRedirect())

    def request(self, endpoint, body=None, expected=200):
        if endpoint not in {'/api/health', '/api/feedback', '/api/slots', '/api/bookings', '/api/bookings/cancel'}:
            raise ValueError('unsupported dedicated test-API endpoint')
        method = 'POST' if body is not None else 'GET'
        encoded = json.dumps(body, allow_nan=False).encode() if body is not None else None
        record = {'method': method, 'endpoint': endpoint, 'outcome': 'intent'}
        self.history.append(record)
        request = urllib.request.Request(self.base + endpoint, data=encoded,
            headers={'Content-Type': 'application/json', 'Accept': 'application/json'}, method=method)
        try:
            try:
                response = self.opener.open(request, timeout=8)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                status = response.code
                data = response.read((2 << 20) + 1)
                if len(data) > 2 << 20 or response.headers.get_content_type() != 'application/json':
                    raise APIError(status, {'error': 'invalid or oversized test-API JSON response'})
                try:
                    value = json.loads(data, object_pairs_hook=unique_json_pairs,
                        parse_constant=lambda value: (_ for _ in ()).throw(ValueError('non-finite JSON')))
                except (ValueError, UnicodeError) as error:
                    raise APIError(status, {'error': 'invalid test-API JSON response'}) from error
            record.update(status=status, responseBytes=len(data))
            if status != expected:
                raise APIError(status, value)
            record['outcome'] = 'confirmed'
            return value
        except (OSError, urllib.error.URLError) as error:
            record['outcome'] = 'uncertain' if method == 'POST' else 'failed'
            raise APIError(None, {'error': 'test API transport failed; no write replay'}) from error
        except Exception:
            record['outcome'] = 'uncertain' if method == 'POST' else 'failed'
            raise


def node_label(node):
    return '\n'.join(node.get(name, '') for name in ['text', 'content-desc', 'hint'])


def run_business(ui, api, profile, report):
    run_id = report.get('runId', '')
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,64}', run_id):
        raise ValueError('explicit safe test-run identity required')
    health = api.request('/api/health')
    if health != {'status': 'ok', 'mode': profile['mode']}:
        raise ValueError('test backend mode is not the approved blueprint mode')
    def check(name):
        report['checks'].append({'name': name, 'passed': True})
    if profile['mode'] == 'feedback':
        if api.request('/api/feedback') != []:
            raise ValueError('feedback requires an empty dedicated test backend')
        ui.tap('提交反馈')
        ui.find('请输入反馈标题。')
        if api.request('/api/feedback') != []:
            raise ValueError('empty-title action unexpectedly created feedback')
        check('native-feedback-required-title-with-no-server-write')
        title, content = 'Device-' + run_id, 'Device-business-' + run_id
        ui.enter('反馈标题', title)
        ui.enter('详细说明', content)
        ui.tap('提交反馈')
        ui.find(title)
        records = api.request('/api/feedback')
        if (not isinstance(records, list) or len(records) != 1
                or records[0].get('title') != title or records[0].get('content') != content
                or not re.fullmatch(r'[a-f0-9]{48}', str(records[0].get('id', '')))):
            raise ValueError('native feedback does not match the exact dedicated HTTPS API receipt')
        check('native-feedback-single-submit-and-independent-https-receipt')
        ui.capture('feedback')
        ui.launch()
        ui.find(title)
        if api.request('/api/feedback') != records:
            raise ValueError('feedback changed across native application relaunch')
        report['testDataRetained'] = {'kind': 'feedback', 'recordId': records[0]['id'], 'runId': run_id}
    else:
        slots = api.request('/api/slots')
        if (not isinstance(slots, list) or len(slots) != 1 or slots[0].get('id') != profile['slotId']
                or slots[0].get('reserved') is not False or not isinstance(slots[0].get('startsAt'), str)):
            raise ValueError('booking requires exactly the declared unreserved dedicated test slot')
        ui.find('可以预约')
        first = api.request('/api/bookings', {'slotId': profile['slotId']}, expected=201)
        first_token = first.get('bookingToken', '') if isinstance(first, dict) else ''
        if not re.fullmatch(r'[a-f0-9]{48}', first_token) or first.get('slotId') != profile['slotId']:
            raise ValueError('test booking receipt invalid; reconcile backend before another run')
        report['pendingBookingCancellation'] = {'bookingToken': first_token, 'slotId': profile['slotId']}
        ui.tap('预约')
        ui.find('This slot is already reserved')
        if api.request('/api/slots')[0].get('reserved') is not True:
            raise ValueError('stale booking conflict did not preserve reservation')
        api.request('/api/bookings/cancel', {'bookingToken': first_token})
        report.pop('pendingBookingCancellation')
        ui.tap('刷新')
        ui.tap('预约')
        label = node_label(ui.find('请保存取消凭据：'))
        matches = re.findall(r'(?<![a-f0-9])[a-f0-9]{48}(?![a-f0-9])', label)
        if len(matches) != 1:
            raise ValueError('native booking did not expose one unambiguous cancellation credential')
        token = matches[0]
        report['pendingBookingCancellation'] = {'bookingToken': token, 'slotId': profile['slotId']}
        ui.tap('已保存')
        ui.find('已预约')
        ui.enter('取消凭据', 'wrong-' + run_id, replace=True)
        ui.tap('取消我的预约')
        ui.find('Booking not found')
        if api.request('/api/slots')[0].get('reserved') is not True:
            raise ValueError('wrong cancellation credential changed the reserved slot')
        check('native-booking-stale-conflict-and-cancellation-ownership')
        ui.enter('取消凭据', token, replace=True)
        ui.tap('取消我的预约')
        ui.find('可以预约')
        if api.request('/api/slots') != slots:
            raise ValueError('native cancellation did not restore the declared slot')
        report.pop('pendingBookingCancellation')
        check('native-booking-opaque-credential-and-valid-cancellation')
        ui.capture('booking')
        ui.launch()
        ui.find('可以预约')
        if api.request('/api/slots') != slots:
            raise ValueError('slot changed across native application relaunch')
    check('native-application-relaunch-and-independent-server-readback')
    report.update(businessVerified=True, liveAPIVerified=True, backendRestartVerified=False,
                  offlineRecoveryVerified=False, upgradeVerified=False)


class AndroidUI:
    def __init__(self, adb, profile, diagnostics, remote_xml, timeout=25):
        self.adb, self.profile, self.diagnostics = adb, profile, Path(diagnostics)
        self.remote_xml, self.timeout = remote_xml, timeout

    def tree(self):
        self.adb('shell', 'uiautomator', 'dump', self.remote_xml)
        value = self.adb('exec-out', 'cat', self.remote_xml)
        if len(value.encode()) > 2 << 20 or '<?xml' not in value:
            raise ValueError('invalid or oversized native UI hierarchy')
        return ET.fromstring(value[value.index('<?xml'):])

    def find(self, text, editable=False):
        deadline = time.monotonic() + self.timeout
        while time.monotonic() < deadline:
            nodes = [node for node in self.tree().iter('node')
                     if node.get('package') == self.profile['package'] and node.get('enabled') == 'true'
                     and text in node_label(node)
                     and (not editable or node.get('class') == 'android.widget.EditText')]
            nodes.sort(key=lambda node: (text not in [node.get(key, '') for key in ['text', 'content-desc', 'hint']],
                                         node.get('clickable') != 'true'))
            if nodes:
                return nodes[0]
            time.sleep(min(0.1, max(0, deadline - time.monotonic())))
        raise AssertionError('approved application did not show expected native UI: ' + text)

    def tap(self, text, editable=False):
        node = self.find(text, editable)
        match = re.fullmatch(r'\[([0-9]+),([0-9]+)\]\[([0-9]+),([0-9]+)\]', node.get('bounds', ''))
        if not match:
            raise ValueError('invalid Android control bounds')
        x1, y1, x2, y2 = map(int, match.groups())
        if x1 >= x2 or y1 >= y2 or max(x2, y2) > 100000:
            raise ValueError('invalid Android control area')
        self.adb('shell', 'input', 'tap', str((x1 + x2) // 2), str((y1 + y2) // 2))

    def enter(self, text, value, replace=False):
        if not re.fullmatch(r'[A-Za-z0-9_-]{1,128}', value):
            raise ValueError('device test inputs require bounded safe ASCII')
        self.tap(text, editable=True)
        if replace:
            self.adb('shell', 'input', 'keyevent', 'KEYCODE_MOVE_END')
            self.adb('shell', 'input', 'keyevent', *(['KEYCODE_DEL'] * 256))
        self.adb('shell', 'input', 'text', value)
        keyboard = self.adb('shell', 'dumpsys', 'input_method')
        if re.search(r'(?:mInputShown|isInputViewShown)=true', keyboard):
            self.adb('shell', 'input', 'keyevent', 'KEYCODE_BACK')

    def launch(self):
        package = self.profile['package']
        self.adb('shell', 'am', 'force-stop', package)
        result = self.adb('shell', 'am', 'start', '-W', '-n', package + '/.MainActivity')
        if 'Status: ok' not in result or re.search(r'(?:^|\n)(?:Error|Exception)', result):
            raise ValueError('native application launch was not confirmed')

    def capture(self, name):
        if not re.fullmatch(r'[a-z0-9-]{1,64}', name):
            raise ValueError('invalid diagnostic name')
        foreground = self.adb('shell', 'dumpsys', 'activity', 'activities')
        if not re.search(r'(?:mResumedActivity|topResumedActivity)[^\n]*\b' + re.escape(self.profile['package']) + r'/', foreground):
            raise ValueError('refuse screenshot while approved application is not foreground')
        tree = self.tree()
        if not any(node.get('package') == self.profile['package'] for node in tree.iter('node')):
            raise ValueError('approved application hierarchy unavailable')
        image = self.adb.raw('exec-out', 'screencap', '-p')
        if not image.startswith(b'\x89PNG\r\n\x1a\n') or len(image) > 16 << 20:
            raise ValueError('invalid native screenshot')
        (self.diagnostics / (name + '.png')).write_bytes(image)
        (self.diagnostics / (name + '.xml')).write_bytes(ET.tostring(tree, encoding='utf-8'))


class ADB:
    def __init__(self, executable, profile, environment):
        self.executable, self.profile, self.environment = executable, profile, environment

    def raw(self, *args):
        # Explicit -H rejects a missing server instead of starting a daemon.
        select = [] if args[:1] == ('devices',) else ['-s', self.profile['serial']]
        return command([self.executable, '-H', '127.0.0.1', '-P', str(self.profile['adbServerPort']),
                        *select, *args], env=self.environment)

    def __call__(self, *args):
        return self.raw(*args).decode('utf-8', errors='replace')


def private_json(path, value):
    fd, temporary = tempfile.mkstemp(prefix='.qa-state-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.write('\n'); stream.flush(); os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def require_test_api(api, profile):
    if api.request('/api/health') != {'status': 'ok', 'mode': profile['mode']}:
        raise ValueError('dedicated HTTPS API does not match the approved business mode')
    if profile['mode'] == 'feedback':
        if api.request('/api/feedback') != []:
            raise ValueError('dedicated feedback API must be empty before installation')
    else:
        slots = api.request('/api/slots')
        if (not isinstance(slots, list) or len(slots) != 1 or slots[0].get('id') != profile['slotId']
                or slots[0].get('reserved') is not False or not isinstance(slots[0].get('startsAt'), str)):
            raise ValueError('dedicated booking API must contain exactly the approved unreserved slot')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['config', 'application', 'adb', 'aapt', 'apksigner', 'output']:
        parser.add_argument('--' + name, type=Path, required=True)
    args = parser.parse_args()
    output = args.output.absolute()
    if output.exists() or output.is_symlink():
        parser.error('report output already exists; no device operation performed')
    if not os.environ.get('IS_ARTIFACT_PATH'):
        parser.error('IS_ARTIFACT_PATH must be supplied by the trusted quality Worker')
    try:
        profile = load_profile(args.config)
        application = args.application.resolve(strict=True)
        if args.config.resolve().is_relative_to(application):
            raise ValueError('administrator device profile must be outside the application source')
        tools = {name: getattr(args, name).resolve(strict=True) for name in ['adb', 'aapt', 'apksigner']}
        if any(path.is_relative_to(application) or not path.is_file() for path in tools.values()):
            raise ValueError('trusted SDK tools must be installed outside application source')
    except (OSError, ValueError) as error:
        parser.error(str(error))
    root = Path(tempfile.mkdtemp(prefix='is-android-device-')).resolve()
    diagnostics = root / 'report'; diagnostics.mkdir(mode=0o700)
    report = {'schemaVersion': 1, 'kind': 'exact-release-apk-usb-device-business',
              'startedAt': datetime.now(timezone.utc).isoformat(), 'runId': 'qa-' + uuid.uuid4().hex,
              'method': 'device', 'verificationEnvironment': 'android-device', 'checks': [], 'cleanup': [],
              'physicalDeviceVerified': False, 'enterpriseIdentityVerified': False, 'storePublished': False,
              'hardwareAttestationVerified': False, 'networkIsolationVerified': False,
              'backendRestartVerified': False, 'offlineRecoveryVerified': False, 'upgradeVerified': False,
              'profile': profile, 'profileBinding': hashlib.sha256(json.dumps(profile, sort_keys=True,
                  separators=(',', ':')).encode()).hexdigest(),
              'evidenceBasis': 'trusted administrator SDK/USB host; tool substitutions cannot prove physical hardware'}
    ownership = {'initiallyAbsent': False, 'installAttempted': False}
    ui, api, adb, lock = None, None, None, None
    remote_xml = '/sdcard/is-device-qa-' + uuid.uuid4().hex + '.xml'
    environment = {key: value for key, value in os.environ.items() if key in {'PATH', 'LANG', 'LC_ALL', 'TMPDIR', 'JAVA_HOME'}}
    environment.update(LANG='C.UTF-8', LC_ALL='C.UTF-8')
    def cancelled(signum, frame):
        signal.signal(signum, signal.SIG_IGN)
        raise InterruptedError('device quality cancelled; reconcile unconfirmed external effects')
    for sig in [signal.SIGINT, signal.SIGTERM]:
        signal.signal(sig, cancelled)
    print(json.dumps({'started': True, 'diagnostics': str(diagnostics), 'runId': report['runId']}), flush=True)
    try:
        artifact = Path(os.environ['IS_ARTIFACT_PATH'])
        if artifact.is_symlink() or not artifact.is_file():
            raise ValueError('exact platform artifact must be a regular APK file')
        artifact = artifact.resolve(strict=True)
        report['archive'] = validate_apk(artifact)
        metadata = json.loads((application / 'application.json').read_text())
        if (metadata.get('blueprint') != 'flutter-go-v1' or metadata.get('mode') != profile['mode']
                or metadata.get('organization', '') + '.' + metadata.get('name', '') != profile['package']):
            raise ValueError('approved package/business differs from exact application source')
        report['application'] = metadata
        report['sourceFiles'] = source_snapshot(application)
        report['sourceDigest'] = hashlib.sha256(json.dumps(report['sourceFiles'], sort_keys=True,
            separators=(',', ':')).encode()).hexdigest()
        badging = command([tools['aapt'], 'dump', 'badging', artifact], env=environment).decode()
        signature = command([tools['apksigner'], 'verify', '--verbose', '--print-certs', artifact], env=environment).decode()
        report['signature'] = validate_release(badging, signature, profile)
        (diagnostics / 'apk-badging.txt').write_text(badging)
        (diagnostics / 'apk-signature.txt').write_text(signature)
        api = HTTPSAPI(profile['apiURL'], ca_file=profile.get('apiCAFile'))
        require_test_api(api, profile)
        with socket.create_connection(('127.0.0.1', profile['adbServerPort']), timeout=3):
            pass
        adb = ADB(tools['adb'], profile, environment)
        # One host lock for this serial, irrespective of the ADB server port.
        lock = device_lock(profile['serial'], 0)
        lock.__enter__()
        report['device'] = physical_identity(adb, profile)
        require_absent_package(adb, profile['package'])
        ownership['initiallyAbsent'] = True
        install_apk = root / 'application.apk'
        prepare_install_apk(artifact, install_apk)
        ownership['installAttempted'] = True
        private_json(diagnostics / 'ownership.json', ownership)
        adb('install', str(install_apk))
        report['installedAPKDigest'] = installed_apk_digest(adb, profile['package'])
        if report['installedAPKDigest'] != report['archive']['sha256']:
            raise ValueError('installed APK bytes differ from the exact platform artifact')
        report['checks'].append({'name': 'approved-usb-profile-version-signature-and-exact-installed-apk', 'passed': True})
        ui = AndroidUI(adb, profile, diagnostics, remote_xml)
        ui.launch()
        run_business(ui, api, profile, report)
        physical_identity(adb, profile)
        if installed_apk_digest(adb, profile['package']) != report['archive']['sha256']:
            raise ValueError('installed bytes changed during business verification')
        if source_snapshot(application) != report['sourceFiles']:
            raise ValueError('application source changed during device verification')
        ui.capture('final')
        report['passed'] = True
    except (Exception, KeyboardInterrupt) as error:
        report.update(passed=False, error=str(error), traceback=traceback.format_exc())
        if getattr(error, 'stderr', None):
            report['toolStderr'] = error.stderr.decode(errors='replace')[-12000:]
        if ui:
            try:
                ui.capture('failure')
            except Exception:
                pass
    finally:
        if adb:
            try:
                result = cleanup_owned(adb, profile, ownership, report.get('archive', {}).get('sha256'))
                report['cleanup'].append({'resource': 'owned-initially-absent-package', 'passed': True, **result})
            except Exception as error:
                report['passed'] = False
                report['cleanup'].append({'resource': 'owned-initially-absent-package', 'passed': False, 'error': str(error)})
            if ui:
                try:
                    physical_identity(adb, profile)
                    adb('shell', 'rm', '-f', remote_xml)
                    report['cleanup'].append({'resource': 'owned-ui-dump', 'passed': True})
                except Exception as error:
                    report['passed'] = False
                    report['cleanup'].append({'resource': 'owned-ui-dump', 'passed': False, 'error': str(error)})
        if lock:
            lock.__exit__(None, None, None)
        report['apiRequests'] = api.history if api else []
        report['finishedAt'] = datetime.now(timezone.utc).isoformat()
        report['physicalDeviceVerified'] = bool(report.get('passed'))
        private_json(diagnostics / 'result.json', report)
        try:
            publish_report(diagnostics, output)
        except Exception as error:
            report.update(passed=False, physicalDeviceVerified=False, reportPublicationError=str(error))
            private_json(diagnostics / 'result.json', report)
        print(json.dumps({'passed': bool(report.get('passed')), 'report': str(output),
                          'diagnostics': str(diagnostics), 'error': report.get('error')}, ensure_ascii=False))
    return 0 if report.get('passed') else 1
