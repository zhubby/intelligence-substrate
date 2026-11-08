#!/usr/bin/env python3
"""Generate a reviewable application; optionally invoke an installed Flutter SDK."""
import argparse
import json
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import xml.etree.ElementTree as ET

parser = argparse.ArgumentParser()
parser.add_argument('destination', type=Path)
parser.add_argument('--name', required=True)
parser.add_argument('--mode', choices=['feedback', 'booking'], default='feedback')
parser.add_argument('--organization', default='com.example')
parser.add_argument('--create-platforms', action='store_true')
parser.add_argument('--gradle-version', help='Optional Gradle distribution approved in blueprint.json')
parser.add_argument('--android-unsigned-release', action='store_true',
                    help='Generate release APKs without a private key for a separate trusted signer')
parser.add_argument('--macos-direct-distribution', action='store_true',
                    help='Opt into a non-sandboxed macOS direct-distribution app; not for the Mac App Store')
args = parser.parse_args()
if not re.fullmatch(r'[a-z][a-z0-9_]{1,49}', args.name):
    parser.error('name must be a lowercase Dart package identifier')
if not re.fullmatch(r'[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+', args.organization):
    parser.error('organization must be a reverse-domain application namespace')
if args.destination.exists():
    parser.error('destination already exists; no files were changed')
if args.macos_direct_distribution and not args.create_platforms:
    parser.error('--macos-direct-distribution requires --create-platforms')
if args.android_unsigned_release and not args.create_platforms:
    parser.error('--android-unsigned-release requires --create-platforms')
template = Path(__file__).resolve().parents[1] / 'blueprints/flutter-go'
gradle = None
if args.gradle_version:
    if not args.create_platforms:
        parser.error('--gradle-version requires --create-platforms')
    gradle = json.loads((template / 'blueprint.json').read_text()).get('gradleDistributions', {}).get(args.gradle_version)
    if not gradle:
        parser.error('Gradle version is not listed in the blueprint')
shutil.copytree(template, args.destination, ignore=shutil.ignore_patterns('.dart_tool', 'build', '.flutter-plugins-dependencies', '__pycache__'))
(args.destination / 'scripts').mkdir()
shutil.copy2(Path(__file__).with_name('build-reference-client.py'), args.destination / 'scripts/build-reference-client.py')
shutil.copy2(Path(__file__).with_name('android_bundle.py'), args.destination / 'scripts/android_bundle.py')
shutil.copy2(Path(__file__).with_name('reference_preview.py'), args.destination / 'scripts/reference_preview.py')
shutil.copy2(Path(__file__).with_name('windows_msix.py'), args.destination / 'scripts/windows_msix.py')
client = args.destination / 'client'
pubspec = (client / 'pubspec.yaml').read_text()
main = (client / 'lib/main.dart').read_text()
settings = {name: (client / name).read_bytes() for name in ['pubspec.lock', 'analysis_options.yaml', 'web/flutter_bootstrap.js'] if (client / name).exists()}
tests = {path.relative_to(client): path.read_bytes() for path in (client / 'test').rglob('*') if path.is_file()} if (client / 'test').exists() else {}
tests = {path: contents.replace(b'package:delivery_app/', ('package:' + args.name + '/').encode()) if path.suffix == '.dart' else contents for path, contents in tests.items()}
if args.create_platforms:
    flutter = shutil.which('flutter')
    if not flutter:
        parser.error('Flutter SDK is not installed; generated sources are preserved')
    subprocess.run([flutter, 'create', '--no-pub', '--platforms=web,ios,android,windows,macos', '--org', args.organization, '--project-name', args.name, str(client.resolve())], check=True)
    if args.android_unsigned_release:
        build_script = client / 'android/app/build.gradle.kts'
        original = build_script.read_text() if build_script.is_file() else ''
        debug_signing = 'signingConfig = signingConfigs.getByName("debug")'
        if original.count(debug_signing) != 1:
            parser.error('unrecognized Android release signing scaffold; generated sources are preserved')
        build_script.write_text(original.replace(debug_signing, 'signingConfig = null'))
    if gradle:
        wrapper = client / 'android/gradle/wrapper/gradle-wrapper.properties'
        lines = [line for line in wrapper.read_text().splitlines() if not line.startswith(('distributionUrl=', 'distributionSha256Sum='))]
        lines += ['distributionUrl=' + gradle['url'].replace(':', '\\:'), 'distributionSha256Sum=' + gradle['sha256']]
        wrapper.write_text('\n'.join(lines) + '\n')
(client / 'pubspec.yaml').write_text(pubspec.replace('name: delivery_app', 'name: ' + args.name))
(client / 'lib/main.dart').write_text(main.replace("defaultValue: 'feedback'", "defaultValue: '" + args.mode + "'"))
for name, contents in settings.items():
    (client / name).write_bytes(contents)
if args.create_platforms:
    # Flutter's counter-app tests do not describe this blueprint. Restore its
    # complete test tree after scaffolding, including files Flutter overwrites.
    shutil.rmtree(client / 'test', ignore_errors=True)
    for path, contents in tests.items():
        (client / path).parent.mkdir(parents=True, exist_ok=True)
        (client / path).write_bytes(contents)
    android_namespace = 'http://schemas.android.com/apk/res/android'
    ET.register_namespace('android', android_namespace)
    for variant in ['main', 'debug']:
        path = client / 'android/app/src' / variant / 'AndroidManifest.xml'
        if not path.exists():
            continue
        tree = ET.parse(path)
        manifest = tree.getroot()
        if variant == 'main':
            if not any(node.get('{' + android_namespace + '}name') == 'android.permission.INTERNET' for node in manifest.findall('uses-permission')):
                ET.SubElement(manifest, 'uses-permission', {'{' + android_namespace + '}name': 'android.permission.INTERNET'})
        else:
            application = manifest.find('application')
            if application is None:
                application = ET.SubElement(manifest, 'application')
            application.set('{' + android_namespace + '}usesCleartextTraffic', 'true')
        tree.write(path, encoding='utf-8', xml_declaration=True)
    for name in ['DebugProfile.entitlements', 'Release.entitlements']:
        path = client / 'macos/Runner' / name
        if path.exists():
            with path.open('rb') as stream:
                entitlements = plistlib.load(stream)
            entitlements['com.apple.security.network.client'] = True
            if args.macos_direct_distribution:
                entitlements['com.apple.security.app-sandbox'] = False
            with path.open('wb') as stream:
                plistlib.dump(entitlements, stream, sort_keys=False)
else:
    for path, contents in tests.items():
        (client / path).write_bytes(contents)
slots = '\nAPP_SLOTS=[{"id":"sample-slot","startsAt":"2026-12-01T09:00:00+08:00","reserved":false}]\n' if args.mode == 'booking' else ''
(args.destination / 'application.env.example').write_text('APP_MODE=' + args.mode + '\nAPP_WEB_ORIGIN=https://app.company.example\nAPP_STATE_FILE=data/state.json\n' + slots)
(args.destination / 'application.json').write_text(json.dumps({
    'name': args.name, 'mode': args.mode, 'organization': args.organization,
    'blueprint': 'flutter-go-v1',
    'gradleVersionOverride': args.gradle_version,
    'androidUnsignedRelease': args.android_unsigned_release,
    'macosDirectDistribution': args.macos_direct_distribution,
}, indent=2) + '\n')
print('Generated sources at', args.destination.resolve())
print('Mode:', args.mode, '| Native builds, signing, real-device checks and store publication remain separate acceptance steps.')
