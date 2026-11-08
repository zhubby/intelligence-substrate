#!/usr/bin/env python3
"""Protected single-APK diagnostic acceptance on an explicitly dedicated emulator."""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import shutil
import socket
import stat
import subprocess
import tarfile
import tempfile
import time
import traceback
import urllib.request
import urllib.error
import xml.etree.ElementTree as ET
import zipfile


from android_quality_common import (
    sha, validate_apk, source_snapshot, device_lock, require_free_port, package_path,
    require_absent_package, installed_apk_digest, install_owned_package,
    prepare_install_apk, validate_signature, command,
)


def require_emulator_identity(device, avd):
    # adb emu avd name can silently return nothing under an isolated Worker
    # HOME because console authentication belongs to the emulator's host user.
    if device('shell', 'getprop', 'ro.boot.qemu.avd_name').strip() != avd or device('shell', 'getprop', 'sys.boot_completed').strip() != '1':
        raise ValueError('selected emulator is not the approved booted AVD')



def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['application', 'adb', 'aapt', 'apksigner', 'output']:
        parser.add_argument('--' + name, type=Path, required=True)
    for name in ['serial', 'avd', 'expected-package']:
        parser.add_argument('--' + name, required=True)
    parser.add_argument('--adb-server-port', type=int, required=True)
    parser.add_argument('--api-port', type=int, required=True)
    parser.add_argument('--release-offline-smoke', action='store_true', help='Only signed installation, launch and offline UI; no business/API acceptance')
    parser.add_argument('--expected-release-signer', help='Exact ephemeral or enterprise certificate SHA256; always identifies actual evidence only')
    parser.add_argument('--feedback-priority', action='store_true', help='Also require real feedback-priority API, native UI, reset and restart behavior')
    args = parser.parse_args()
    if bool(args.expected_release_signer) != args.release_offline_smoke or args.expected_release_signer and not re.fullmatch(r'[a-f0-9]{64}', args.expected_release_signer):
        parser.error('release offline smoke requires one exact lowercase SHA256 certificate')
    if args.feedback_priority and args.release_offline_smoke:
        parser.error('priority verification requires the complete business flow')
    if not re.fullmatch(r'emulator-[0-9]+', args.serial) or not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}', args.avd):
        parser.error('explicit dedicated emulator serial/AVD required')
    if not 1024 <= args.adb_server_port <= 65535 or not 1024 <= args.api_port <= 65535:
        parser.error('explicit unprivileged loopback ports required')
    if not re.fullmatch(r'com\.intelligencesubstrate\.quality\.android_(feedback|booking)', args.expected_package):
        parser.error('only explicitly named diagnostic reference packages are supported')
    if not os.environ.get('IS_ARTIFACT_PATH'):
        parser.error('IS_ARTIFACT_PATH is required')
    output = args.output.absolute()
    if output.exists() or output.is_symlink():
        parser.error('report output already exists')
    root = Path(tempfile.mkdtemp(prefix='is-android-quality-')).resolve()
    diagnostics = root / 'report'
    diagnostics.mkdir()
    report = {'schemaVersion': 1, 'kind': 'actual-single-apk-and-source-go-api', 'startedAt': datetime.now(timezone.utc).isoformat(),
              'physicalDevice': False, 'storePublished': False, 'verificationEnvironment': 'simulator', 'method': 'simulator',
              'serial': args.serial, 'avd': args.avd, 'checks': [], 'cleanup': [], 'networkIsolationVerified': False}
    if args.release_offline_smoke:
        report.update(kind='actual-release-apk-installation-and-offline-ui', businessVerified=False,
                      liveAPIVerified=False, enterpriseIdentityVerified=False)
    state = {'installAttempted': False}
    child, api_log, locked, selected = None, None, None, False
    package = args.expected_package
    adb = [str(args.adb.resolve()), '-P', str(args.adb_server_port), '-s', args.serial]
    adb_environment = {**os.environ, 'ADB_SERVER_SOCKET': 'tcp:127.0.0.1:' + str(args.adb_server_port),
                       'ANDROID_ADB_SERVER_PORT': str(args.adb_server_port)}
    def device(*argv):
        return command([*adb, *argv], env=adb_environment).decode('utf-8', errors='replace')
    remote_xml = '/sdcard/is-quality-' + str(os.getpid()) + '.xml'
    def tree():
        device('shell', 'uiautomator', 'dump', remote_xml)
        xml = device('exec-out', 'cat', remote_xml)
        return ET.fromstring(xml[xml.index('<?xml'):])
    def label(node):
        return '\n'.join(node.get(k, '') for k in ['text', 'content-desc', 'hint'])
    def find(text, editable=False):
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            nodes = [n for n in tree().iter('node') if text in label(n) and n.get('enabled') == 'true'
                     and (not editable or n.get('class') == 'android.widget.EditText')]
            nodes.sort(key=lambda n: n.get('clickable') != 'true')
            if nodes:
                return nodes[0]
        raise AssertionError('Android UI did not show ' + text)
    def tap(text, editable=False):
        node = find(text, editable)
        bounds = list(map(int, re.findall(r'\d+', node.get('bounds', ''))))
        if len(bounds) != 4 or bounds[2] <= bounds[0] or bounds[3] <= bounds[1]:
            raise ValueError('invalid Android UI bounds')
        device('shell', 'input', 'tap', str((bounds[0] + bounds[2]) // 2), str((bounds[1] + bounds[3]) // 2))
    def enter(text, value, replace=False):
        if not re.fullmatch(r'[a-zA-Z0-9_-]+', value):
            raise ValueError('diagnostic Android input requires safe ASCII')
        tap(text, editable=True)
        if replace:
            device('shell', 'input', 'keyevent', 'KEYCODE_MOVE_END')
            device('shell', 'input', 'keyevent', *(['KEYCODE_DEL'] * 64))
        device('shell', 'input', 'text', value)
        device('shell', 'input', 'keyevent', 'KEYCODE_BACK')
    def capture(name):
        (diagnostics / (name + '.png')).write_bytes(command([*adb, 'exec-out', 'screencap', '-p'], env=adb_environment))
        (diagnostics / (name + '.xml')).write_bytes(ET.tostring(tree(), encoding='utf-8'))
    def api(endpoint, body=None):
        request = urllib.request.Request('http://127.0.0.1:' + str(args.api_port) + endpoint,
            data=json.dumps(body).encode() if body is not None else None, headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=3) as response:
            return json.load(response)
    def stop_backend():
        nonlocal child
        if child is not None:
            try:
                os.killpg(child.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait(timeout=5)
            child = None
    def start_backend():
        nonlocal child
        require_free_port(args.api_port)
        child = subprocess.Popen(['rtk', 'proxy', str(root / 'api')], stdout=subprocess.DEVNULL, stderr=api_log,
            start_new_session=True, env={**os.environ, 'APP_MODE': metadata['mode'], 'APP_LISTEN': '127.0.0.1:' + str(args.api_port),
            'APP_STATE_FILE': str(root / 'state.json'), 'APP_SLOTS': json.dumps([{'id': 'first', 'startsAt': '2026-12-01T09:00:00+08:00', 'reserved': False}])})
        deadline = time.monotonic() + 12
        while time.monotonic() < deadline:
            if child.poll() is not None:
                raise ValueError('owned API exited during startup')
            try:
                if api('/api/health'):
                    return
            except (OSError, ValueError):
                time.sleep(0.1)
        raise ValueError('owned API did not become healthy')
    def restart_backend():
        stop_backend()
        start_backend()
    def launch():
        device('shell', 'am', 'force-stop', package)
        device('shell', 'am', 'start', '-W', '-n', package + '/.MainActivity')
    def installed_digest():
        return installed_apk_digest(device, package)
    def check(name):
        report['checks'].append({'name': name, 'passed': True})
    def cancel(signum, frame):
        signal.signal(signum, signal.SIG_IGN)
        raise InterruptedError('Android quality cancelled')
    for sig in [signal.SIGINT, signal.SIGTERM]:
        signal.signal(sig, cancel)
    try:
        artifact = Path(os.environ['IS_ARTIFACT_PATH']).resolve(strict=True)
        report['archive'] = validate_apk(artifact)
        application = args.application.resolve(strict=True)
        metadata = json.loads((application / 'application.json').read_text())
        if metadata.get('blueprint') != 'flutter-go-v1' or metadata.get('mode') not in {'feedback', 'booking'} or metadata.get('organization', '') + '.' + metadata.get('name', '') != package:
            raise ValueError('source identity does not match the protected expected package')
        report['application'] = metadata
        if args.feedback_priority and metadata['mode'] != 'feedback':
            raise ValueError('priority verification requires a feedback application')
        report['feedbackPriorityRequired'] = args.feedback_priority
        report['sourceFiles'] = source_snapshot(application)
        report['sourceDigest'] = hashlib.sha256(json.dumps(report['sourceFiles'], sort_keys=True, separators=(',', ':')).encode()).hexdigest()
        badging = command([args.aapt.resolve(), 'dump', 'badging', artifact]).decode()
        if not re.search(r"^package: name='" + re.escape(package) + r"'", badging) or "native-code: 'arm64-v8a'" not in badging:
            raise ValueError('APK identity/ABI does not match diagnostic inputs')
        signer = command([args.apksigner.resolve(), 'verify', '--verbose', '--print-certs', artifact]).decode()
        signature = validate_signature(signer, badging, args.expected_release_signer)
        (diagnostics / 'apk-badging.txt').write_text(badging)
        (diagnostics / 'apk-signature.txt').write_text(signer)
        report['signing'] = signature['kind']
        report['signature'] = signature
        require_free_port(args.api_port)
        with socket.create_connection(('127.0.0.1', args.adb_server_port), timeout=3):
            pass
        locked = device_lock(args.serial, args.adb_server_port)
        locked.__enter__()
        require_emulator_identity(device, args.avd)
        require_absent_package(device, package)
        selected = True
        report['device'] = {k: device('shell', 'getprop', k).strip() for k in ['ro.build.version.sdk', 'ro.product.cpu.abi', 'ro.build.fingerprint']}
        if not args.release_offline_smoke:
            command(['go', 'build', '-trimpath', '-o', root / 'api', '.'], cwd=application / 'backend')
            report['backendBinarySHA256'] = sha(root / 'api')
            api_log = (diagnostics / 'api.log').open('ab')
            start_backend()
        install_apk = root / 'application.apk'
        prepare_install_apk(artifact, install_apk)
        install_owned_package(device, install_apk, state)
        report['installedAPKDigest'] = installed_digest()
        if report['installedAPKDigest'] != report['archive']['sha256']:
            raise ValueError('installed APK differs from the exact platform package')
        check('installed-apk-byte-digest-matches-tested-artifact')
        launch()
        if args.release_offline_smoke:
            find('连接失败，请检查网络后刷新。')
            tap('刷新')
            find('连接失败，请检查网络后刷新。')
            if metadata['mode'] == 'feedback':
                tap('提交反馈')
                find('请输入反馈标题。')
            check('release-signature-non-debuggable-and-native-offline-ui')
        elif metadata['mode'] == 'feedback':
            tap('提交反馈')
            find('请输入反馈标题。')
            assert api('/api/feedback') == []
            enter('反馈标题', 'Android-feedback')
            enter('详细说明', 'Real-emulator-submit')
            tap('提交反馈')
            find('Android-feedback')
            records = api('/api/feedback')
            assert len(records) == 1 and records[0]['title'] == 'Android-feedback' and records[0]['content'] == 'Real-emulator-submit'
            restart_backend()
            assert api('/api/feedback') == records
            launch()
            find('Android-feedback')
            capture('feedback')
            check('feedback-native-validation-submit-and-backend-restart')
            stop_backend()
            tap('刷新')
            find('连接失败，请检查网络后刷新。')
            start_backend()
            api('/api/feedback', {'title': 'Android-network-recovered', 'content': 'Fresh-server-record'})
            tap('刷新')
            find('Android-network-recovered')
        else:
            find('可以预约')
            first = api('/api/bookings', {'slotId': 'first'})
            tap('预约')
            find('This slot is already reserved')
            api('/api/bookings/cancel', {'bookingToken': first['bookingToken']})
            tap('刷新')
            tap('预约')
            token = re.search(r'[a-f0-9]{48}', label(find('请保存取消凭据：'))).group(0)
            assert token not in (root / 'state.json').read_text()
            tap('已保存')
            find('已预约')
            assert token in label(find('取消凭据', editable=True))
            restart_backend()
            assert api('/api/slots')[0]['reserved'] is True
            check('booking-native-stale-conflict-opaque-token-and-backend-restart')
            enter('取消凭据', 'wrong-token', replace=True)
            tap('取消我的预约')
            find('Booking not found')
            assert api('/api/slots')[0]['reserved'] is True
            enter('取消凭据', token, replace=True)
            tap('取消我的预约')
            find('可以预约')
            restart_backend()
            assert api('/api/slots')[0]['reserved'] is False
            check('booking-native-cancellation-ownership-and-persistence')
            stop_backend()
            tap('刷新')
            find('连接失败，请检查网络后刷新。')
            start_backend()
            tap('刷新')
            find('可以预约')
        if not args.release_offline_smoke:
            check('native-api-unavailable-and-refresh-recovery')
        if args.feedback_priority:
            default = api('/api/feedback', {'title': 'Android-priority-default-API', 'content': 'Omitted priority'})
            assert default.get('priority') == 'normal', 'omitted priority must be persisted as normal'
            before = api('/api/feedback')
            for priority in ['administrator', '', 'NORMAL', ' urgent', 'urgent ']:
                try:
                    api('/api/feedback', {'title': 'Android-priority-invalid', 'content': 'Reject invalid value', 'priority': priority})
                except urllib.error.HTTPError as error:
                    assert error.code == 422, 'explicit invalid priority must return 422: ' + repr(priority)
                else:
                    raise AssertionError('invalid priority was accepted: ' + repr(priority))
                assert api('/api/feedback') == before, 'invalid priority created feedback'
            check('feedback-priority-default-and-invalid-api')
            tap('优先级')
            tap('紧急')
            enter('反馈标题', 'Android-urgent-feedback')
            enter('详细说明', 'Real-native-priority')
            before_failure = api('/api/feedback')
            stop_backend()
            tap('提交反馈')
            find('提交结果尚未确认，请先刷新核对，避免重复提交。')
            start_backend()
            assert api('/api/feedback') == before_failure, 'failed submission created feedback'
            tap('提交反馈')
            find('Android-urgent-feedback')
            find('紧急')
            records = api('/api/feedback')
            urgent = [item for item in records if item['title'] == 'Android-urgent-feedback']
            assert len(urgent) == 1 and urgent[0].get('priority') == 'urgent'
            assert urgent[0]['content'] == 'Real-native-priority'
            check('feedback-priority-native-failed-submit-retains-input-and-selection')
            enter('反馈标题', 'Android-normal-feedback')
            enter('详细说明', 'Reset-after-success')
            tap('提交反馈')
            find('Android-normal-feedback')
            records = api('/api/feedback')
            normal = [item for item in records if item['title'] == 'Android-normal-feedback']
            assert len(normal) == 1 and normal[0].get('priority') == 'normal'
            restart_backend()
            assert api('/api/feedback') == records
            launch()
            find('Android-urgent-feedback')
            find('紧急')
            capture('feedback-priority')
            check('feedback-priority-native-ui-reset-and-backend-restart')
        capture('final')
        report['passed'] = True
    except (Exception, KeyboardInterrupt) as error:
        report['passed'] = False
        report['error'] = str(error)
        report['traceback'] = traceback.format_exc()
        if isinstance(error, subprocess.CalledProcessError):
            report['toolStderr'] = (error.stderr or b'').decode(errors='replace')[-12000:]
        if selected:
            try:
                capture('failure')
            except Exception:
                pass
    finally:
        try:
            stop_backend()
            report['cleanup'].append({'resource': 'owned-go-backend', 'stopped': True})
        except Exception as error:
            report['passed'] = False
            report['cleanup'].append({'resource': 'owned-go-backend', 'error': str(error)})
        if api_log:
            api_log.close()
        if selected:
            try:
                if state['installAttempted'] and package_path(device, package):
                    if installed_digest() != report['archive']['sha256']:
                        raise ValueError('refuse uninstalling a package with different bytes')
                    if device('uninstall', package).strip() != 'Success':
                        raise ValueError('owned package uninstall was not confirmed')
                require_absent_package(device, package)
                device('shell', 'rm', '-f', remote_xml)
                report['cleanup'].append({'resource': 'initially-absent-diagnostic-package', 'stopped': True})
            except Exception as error:
                report['passed'] = False
                report['cleanup'].append({'resource': 'initially-absent-diagnostic-package', 'error': str(error)})
        if locked:
            locked.__exit__(None, None, None)
        report['finishedAt'] = datetime.now(timezone.utc).isoformat()
        (diagnostics / 'result.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        output.parent.mkdir(parents=True, exist_ok=True)
        fd, temporary = tempfile.mkstemp(prefix='.android-quality-report-', dir=output.parent)
        os.close(fd)
        try:
            with tarfile.open(temporary, 'w:gz') as archive:
                for file in sorted(diagnostics.iterdir()):
                    if file.is_symlink() or not file.is_file():
                        raise ValueError('unsafe report entry')
                    archive.add(file, arcname=file.name, recursive=False)
            os.link(temporary, output)
        finally:
            os.unlink(temporary)
        print(json.dumps({'passed': report.get('passed', False), 'report': str(output), 'diagnostics': str(diagnostics),
                          'artifactSHA256': report.get('archive', {}).get('sha256'), 'checks': report['checks'], 'error': report.get('error')}))
    return 0 if report.get('passed') else 1


if __name__ == '__main__':
    raise SystemExit(main())
