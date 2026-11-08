"""Deterministic SDK/USB/UI boundary for CLI tests; never hardware evidence."""
import base64
import hashlib
import json
from pathlib import Path
import re
import ssl
import sys
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET


role, control_path = sys.argv[1:3]
control = json.loads(Path(control_path).read_text())
profile = control['profile']
state_path = Path(control['state'])
state = json.loads(state_path.read_text())
args = sys.argv[3:]
package = profile['package']


def save():
    state_path.write_text(json.dumps(state))


def api(path, body=None):
    request = urllib.request.Request(profile['apiURL'] + '/api/' + path,
        data=json.dumps(body).encode() if body is not None else None, headers={'Content-Type': 'application/json'})
    try:
        response = urllib.request.urlopen(request, context=ssl.create_default_context(cafile=control['certificate']), timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        value = json.load(response)
        if response.code >= 300:
            state['message'] = value['error']
            return None
        return value


if role == 'aapt':
    print(f"package: name='{package}' versionCode='1' versionName='1.0.0'")
    print("native-code: 'arm64-v8a'")
    print(f"launchable-activity: name='{package}.MainActivity' label='QA' icon=''")
    raise SystemExit(0)
if role == 'apksigner':
    print('Signer #1 certificate DN: CN=Test Release')
    print('Signer #1 certificate SHA-256 digest: ' + 'a' * 64)
    print('Verified using v2 scheme (APK Signature Scheme v2): true')
    print('Verified using v3 scheme (APK Signature Scheme v3): true')
    raise SystemExit(0)
if args[:2] != ['-H', '127.0.0.1'] or args[2:4] != ['-P', str(profile['adbServerPort'])]:
    raise SystemExit('explicit host/port required; never use a real device')
args = args[4:]
if args[:1] == ['-s']:
    if args[1] != profile['serial']:
        raise SystemExit('unexpected serial')
    args = args[2:]
if args == ['devices', '-l']:
    print('List of devices attached\n' + profile['serial'] + ' device usb:1-2 product:qa model:QA_Phone transport_id:1')
elif args[:2] == ['shell', 'getprop']:
    props = {'ro.product.manufacturer': profile['manufacturer'], 'ro.product.model': profile['model'],
        'ro.build.fingerprint': profile['fingerprint'], 'ro.build.version.sdk': str(profile['sdk']),
        'ro.product.cpu.abilist': 'arm64-v8a', 'sys.boot_completed': '1'}
    print(props.get(args[2], ''))
elif args[:3] == ['shell', 'pm', 'path']:
    if state['installed']:
        print('package:/data/app/~~abc/com.company-123/base.apk')
elif args[:2] == ['exec-out', 'sha256sum']:
    print(state['installed'] + '  /data/app/~~abc/com.company-123/base.apk')
elif args[:1] == ['install']:
    intent = json.loads((Path(args[-1]).parent / 'report/ownership.json').read_text())
    if intent != {'initiallyAbsent': True, 'installAttempted': True}:
        raise SystemExit('installation intent was not persisted before dispatch')
    state['installed'] = hashlib.sha256(Path(args[-1]).read_bytes()).hexdigest()
    if control['scenario'] == 'changed-install':
        state['installed'] = 'b' * 64
    state['installCount'] += 1; save()
    if control['scenario'] == 'lost-install':
        raise SystemExit('installation reply lost after external acceptance')
    print('Success')
elif args[:1] == ['uninstall']:
    state['installed'] = None; state['uninstallCount'] += 1; save(); print('Success')
elif args[:3] == ['shell', 'am', 'force-stop']:
    state['foreground'] = False; save()
elif args[:3] == ['shell', 'am', 'start']:
    state['foreground'] = True
    state['items'] = api('feedback' if profile['mode'] == 'feedback' else 'slots')
    save(); print('Status: ok')
elif args[:3] == ['shell', 'dumpsys', 'input_method']:
    print('mInputShown=' + ('true' if state.get('keyboard') else 'false'))
elif args[:3] == ['shell', 'dumpsys', 'activity']:
    print('mResumedActivity: ActivityRecord{123 u0 ' + package + '/.MainActivity}')
elif args[:3] == ['shell', 'uiautomator', 'dump'] or args[:3] == ['shell', 'rm', '-f']:
    pass
elif args[:2] == ['exec-out', 'cat']:
    tree = ET.Element('hierarchy')
    def node(text, index=0, editable=False):
        return ET.SubElement(tree, 'node', {'package': package, 'text': state['fields'].get(text, '') if editable else text,
            'content-desc': text if editable else '', 'class': 'android.widget.EditText' if editable else 'android.widget.Button',
            'enabled': 'true', 'clickable': 'true' if index else 'false', 'bounds': '[0,' + str(index * 100) + '][100,' + str(index * 100 + 80) + ']'})
    node('刷新', 1)
    if profile['mode'] == 'feedback':
        node('提交反馈', 2); node('反馈标题', 3, True); node('详细说明', 4, True)
        for item in state['items'] or []:
            node(item['title'])
    else:
        reserved = bool(state['items'] and state['items'][0]['reserved'])
        node('已预约' if reserved else '可以预约')
        node('预约', 5); node('取消凭据', 6, True); node('取消我的预约', 7)
        if state.get('token') and state.get('dialog'):
            node('请保存取消凭据：' + state['token']); node('已保存', 8)
    if state['message']:
        node(state['message'])
    print('<?xml version="1.0"?>' + ET.tostring(tree, encoding='unicode'))
elif args[:3] == ['shell', 'input', 'tap']:
    index = int(args[4]) // 100
    if index in {3, 4, 6}:
        state['focus'] = {3: '反馈标题', 4: '详细说明', 6: '取消凭据'}[index]
        state['keyboard'] = True
    elif index == 1:
        state['items'] = api('feedback' if profile['mode'] == 'feedback' else 'slots'); state['message'] = ''
    elif index == 2:
        if not state['fields'].get('反馈标题'):
            state['message'] = '请输入反馈标题。'
        else:
            result = api('feedback', {'title': state['fields']['反馈标题'], 'content': state['fields'].get('详细说明', '')})
            if result:
                state['items'] = api('feedback'); state['message'] = ''
    elif index == 5:
        result = api('bookings', {'slotId': profile['slotId']})
        if result:
            state['token'] = result['bookingToken']; state['dialog'] = True
            state['fields']['取消凭据'] = state['token']; state['message'] = ''; state['items'] = api('slots')
    elif index == 7:
        result = api('bookings/cancel', {'bookingToken': state['fields'].get('取消凭据', '')})
        if result:
            state['items'] = api('slots'); state['message'] = ''
    elif index == 8:
        state['dialog'] = False
    save()
elif args[:3] == ['shell', 'input', 'text']:
    field = state['focus']; state['fields'][field] = state['fields'].get(field, '') + args[3]; save()
elif args[:3] == ['shell', 'input', 'keyevent']:
    if 'KEYCODE_DEL' in args:
        field = state['focus']; state['fields'][field] = state['fields'].get(field, '')[:-args.count('KEYCODE_DEL')]
    if 'KEYCODE_BACK' in args:
        state['keyboard'] = False
    save()
elif args[:2] == ['exec-out', 'screencap']:
    sys.stdout.buffer.write(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII='))
else:
    raise SystemExit('unsupported fixture command: ' + repr(args))
