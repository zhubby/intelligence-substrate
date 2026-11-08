#!/usr/bin/env python3
"""Exercise real reference APKs on an explicitly selected, dedicated emulator."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time
import urllib.request
import xml.etree.ElementTree as ET


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['feedback', 'booking', 'feedback-manifest', 'booking-manifest', 'adb', 'aapt', 'apksigner']:
        parser.add_argument('--' + name, required=True, type=Path)
    parser.add_argument('--serial', required=True)
    parser.add_argument('--avd', required=True, help='Dedicated AVD name; existing user devices are not selected automatically')
    args = parser.parse_args()
    if not re.fullmatch(r'emulator-\d+', args.serial):
        parser.error('this diagnostic accepts an explicitly selected emulator only')
    repo = Path(__file__).resolve().parents[1]
    root = Path(tempfile.mkdtemp(prefix='reference-android-', dir=repo / 'work'))
    report = {'kind': 'actual-android-emulator-and-go-api', 'startedAt': datetime.now(timezone.utc).isoformat(),
              'serial': args.serial, 'avd': args.avd, 'physicalDevice': False, 'storePublished': False,
              'artifacts': {}, 'checks': []}
    applications = []
    selected = False
    remote_xml = '/sdcard/is-reference-' + str(os.getpid()) + '.xml'
    adb = [str(args.adb.resolve()), '-s', args.serial]

    def command(argv, **kwargs):
        return subprocess.run([str(a) for a in argv], check=True, capture_output=True, timeout=60, **kwargs).stdout

    def device(*argv):
        return command([*adb, *argv]).decode('utf-8', errors='replace')

    def tree():
        device('shell', 'uiautomator', 'dump', remote_xml)
        data = device('exec-out', 'cat', remote_xml)
        (root / 'latest-ui.xml').write_text(data)
        return ET.fromstring(data[data.index('<?xml'):])

    def label(node):
        # Android exposes an empty Flutter text field's label as hintText.
        return '\n'.join(node.get(key, '') for key in ['text', 'content-desc', 'hint'])

    def find(text, editable=False, timeout=25):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            nodes = [n for n in tree().iter('node') if text in label(n) and n.get('enabled') == 'true']
            if editable:
                nodes = [n for n in nodes if n.get('class') == 'android.widget.EditText']
            nodes.sort(key=lambda n: n.get('clickable') != 'true')
            if nodes:
                return nodes[0]
        raise AssertionError('Android UI did not show ' + text)

    def tap(text, editable=False):
        node = find(text, editable)
        bounds = [int(n) for n in re.findall(r'\d+', node.get('bounds', ''))]
        assert len(bounds) == 4 and bounds[2] > bounds[0] and bounds[3] > bounds[1], node.attrib
        device('shell', 'input', 'tap', str((bounds[0] + bounds[2]) // 2), str((bounds[1] + bounds[3]) // 2))

    def enter(text, value, replace=False):
        assert re.fullmatch(r'[a-zA-Z0-9_-]+', value), 'test input must be safe ASCII for Android input text'
        tap(text, editable=True)
        if replace:
            device('shell', 'input', 'keyevent', 'KEYCODE_MOVE_END')
            device('shell', 'input', 'keyevent', *(['KEYCODE_DEL'] * 64))
        device('shell', 'input', 'text', value)
        device('shell', 'input', 'keyevent', 'KEYCODE_BACK')

    def capture(name):
        (root / (name + '.png')).write_bytes(command([*adb, 'exec-out', 'screencap', '-p']))
        (root / (name + '.xml')).write_bytes(ET.tostring(tree(), encoding='utf-8'))

    def api(application, endpoint, body=None):
        request = urllib.request.Request(application['api'] + endpoint,
                                         data=json.dumps(body).encode() if body is not None else None,
                                         headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(request, timeout=3) as response:
            return json.load(response)

    def stop(application):
        child = application.get('child')
        if child is not None and child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)
        if application.get('log'):
            application['log'].close()

    def start(application):
        # The selected APK has a fixed API endpoint. Refuse to use another service.
        with socket.socket() as probe:
            probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            probe.bind(('127.0.0.1', application['port']))
        application['log'] = (application['directory'] / 'api.log').open('ab')
        application['child'] = subprocess.Popen([str(application['binary'])], stdout=subprocess.DEVNULL,
            stderr=application['log'], env={**os.environ, 'APP_MODE': application['mode'],
            'APP_LISTEN': '127.0.0.1:' + str(application['port']), 'APP_STATE_FILE': str(application['state']),
            'APP_SLOTS': json.dumps([{'id': 'first', 'startsAt': '2026-12-01T09:00:00+08:00', 'reserved': False}])})
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                if api(application, '/api/health'):
                    return
            except (OSError, ValueError):
                time.sleep(0.2)
        raise AssertionError('actual Go API failed to start')

    def restart(application):
        stop(application)
        start(application)

    def launch(application):
        device('shell', 'am', 'force-stop', application['package'])
        device('shell', 'am', 'start', '-W', '-n', application['package'] + '/.MainActivity')

    try:
        assert device('emu', 'avd', 'name').splitlines()[0] == args.avd, 'selected emulator is not the dedicated AVD'
        assert device('shell', 'getprop', 'sys.boot_completed').strip() == '1'
        selected = True
        report['device'] = {key: device('shell', 'getprop', key).strip()
                            for key in ['ro.build.version.sdk', 'ro.product.cpu.abi', 'ro.build.fingerprint']}
        for mode in ['feedback', 'booking']:
            source = getattr(args, mode).resolve()
            manifest_path = getattr(args, mode + '_manifest').resolve()
            manifest = json.loads(manifest_path.read_text())
            assert manifest['mode'] == mode and manifest['target'] == 'android-debug'
            assert manifest['localTesting'] is True and manifest['signing'] == 'development-debug-key'
            port = 18110 if mode == 'feedback' else 18111
            assert manifest['apiBaseURL'] == 'http://10.0.2.2:' + str(port)
            artifact = manifest_path.parent / manifest['artifact']['file']
            digest = hashlib.sha256(artifact.read_bytes()).hexdigest()
            assert digest == manifest['artifact']['sha256']
            badging = command([args.aapt.resolve(), 'dump', 'badging', artifact]).decode()
            package = re.search(r"^package: name='([^']+)'", badging).group(1)
            assert package.startswith('com.intelligencesubstrate.reference.'), 'only task reference application IDs are allowed'
            assert "native-code: 'arm64-v8a'" in badging
            signer = command([args.apksigner.resolve(), 'verify', '--verbose', '--print-certs', artifact]).decode()
            assert 'CN=Android Debug' in signer, 'expected a diagnostic APK signer'
            directory = root / mode
            directory.mkdir()
            (directory / 'apk-badging.txt').write_text(badging)
            (directory / 'apk-signature.txt').write_text(signer)
            application = {'mode': mode, 'package': package, 'directory': directory,
                           'binary': directory / 'api', 'state': directory / 'state.json',
                           'port': port, 'api': 'http://127.0.0.1:' + str(port)}
            applications.append(application)
            command(['go', 'build', '-trimpath', '-o', application['binary'], '.'], cwd=source / 'backend')
            start(application)
            device('install', '-r', '-t', str(artifact))
            report['artifacts'][mode] = {'sha256': digest, 'package': package,
                                         'signing': 'development-debug-key', 'manifest': str(manifest_path)}

        feedback, booking = applications
        launch(feedback)
        tap('提交反馈')
        find('请输入反馈标题。')
        assert api(feedback, '/api/feedback') == []
        enter('反馈标题', 'Android-feedback')
        enter('详细说明', 'Real-emulator-submit')
        tap('提交反馈')
        find('Android-feedback')
        records = api(feedback, '/api/feedback')
        assert len(records) == 1 and records[0]['title'] == 'Android-feedback'
        assert records[0]['content'] == 'Real-emulator-submit'
        restart(feedback)
        assert api(feedback, '/api/feedback') == records
        launch(feedback)
        find('Android-feedback')
        capture('feedback-android')
        report['checks'].append({'name': 'feedback-native-validation-submit-list-and-api-restart', 'passed': True})
        stop(feedback)
        tap('刷新')
        find('连接失败，请检查网络后刷新。')
        start(feedback)
        api(feedback, '/api/feedback', {'title': 'Android-network-recovered', 'content': 'Fresh-server-record'})
        tap('刷新')
        find('Android-network-recovered')
        report['checks'].append({'name': 'feedback-native-api-unavailable-and-refresh-recovery', 'passed': True})

        launch(booking)
        find('可以预约')
        # Reserve externally after the native client loaded; its tap must expose
        # the actual server's conflict rather than overwrite another reservation.
        first = api(booking, '/api/bookings', {'slotId': 'first'})
        tap('预约')
        find('This slot is already reserved')
        api(booking, '/api/bookings/cancel', {'bookingToken': first['bookingToken']})
        tap('刷新')
        tap('预约')
        success = find('请保存取消凭据：')
        token = re.search(r'[a-f0-9]{48}', label(success)).group(0)
        assert token not in booking['state'].read_text()
        tap('已保存')
        find('已预约')
        field = find('取消凭据', editable=True)
        assert token in label(field), 'active client did not retain its cancellation credential'
        restart(booking)
        assert api(booking, '/api/slots')[0]['reserved'] is True
        capture('booking-android-reserved')
        report['checks'].append({'name': 'booking-native-conflict-token-and-api-restart', 'passed': True})
        enter('取消凭据', 'wrong-token', replace=True)
        tap('取消我的预约')
        find('Booking not found')
        assert api(booking, '/api/slots')[0]['reserved'] is True
        enter('取消凭据', token, replace=True)
        tap('取消我的预约')
        find('可以预约')
        restart(booking)
        assert api(booking, '/api/slots')[0]['reserved'] is False
        capture('booking-android-cancelled')
        report['checks'].append({'name': 'booking-native-cancellation-ownership-and-api-restart', 'passed': True})
        report['passed'] = True
    except Exception as error:
        import traceback
        report['passed'] = False
        report['error'] = str(error)
        report['traceback'] = traceback.format_exc()
        if selected:
            try:
                capture('failure')
            except Exception:
                pass
    finally:
        for application in applications:
            stop(application)
        if selected:
            try:
                device('shell', 'rm', '-f', remote_xml)
            except Exception:
                pass
        report['finishedAt'] = datetime.now(timezone.utc).isoformat()
        (root / 'result.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
        print(json.dumps({'root': str(root), **report}, ensure_ascii=False, indent=2))
    return 0 if report.get('passed') else 1


if __name__ == '__main__':
    raise SystemExit(main())
