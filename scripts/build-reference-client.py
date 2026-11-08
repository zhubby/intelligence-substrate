#!/usr/bin/env python3
"""Build one reference client and emit an uploadable artifact with provenance."""
import argparse
from datetime import datetime, timezone
import hashlib
import io
import ipaddress
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from urllib.parse import urlsplit
import android_bundle
import reference_preview
import windows_msix


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        while data := stream.read(1024 * 1024):
            digest.update(data)
    return digest.hexdigest()


def bundle_entries(bundle):
    """Keep framework links, but never archive an escape or a special file."""
    if bundle.is_symlink() or not bundle.is_dir():
        raise ValueError('invalid application bundle directory')
    root = bundle.resolve()
    entries = [bundle, *sorted(bundle.rglob('*'))]
    for path in entries:
        mode = path.lstat().st_mode
        if stat.S_ISLNK(mode):
            if os.path.isabs(os.readlink(path)):
                raise ValueError('absolute link in application bundle')
            try:
                resolved = path.resolve(strict=True)
            except (OSError, RuntimeError) as error:
                raise ValueError('broken or cyclic link in application bundle') from error
            if not resolved.is_relative_to(root):
                raise ValueError('link escapes application bundle')
            if not (resolved.is_dir() or resolved.is_file()):
                raise ValueError('link to special entry in application bundle')
        elif not (stat.S_ISDIR(mode) or stat.S_ISREG(mode)):
            raise ValueError('special entry in application bundle')
    return entries


def inspect_apple(bundle, target, native):
    entries = bundle_entries(bundle)
    macos = target == 'macos-unsigned'
    contents = bundle / 'Contents' if macos else bundle
    try:
        info = plistlib.loads((contents / 'Info.plist').read_bytes())
        expected_platform = 'MacOSX' if macos else 'iPhoneOS' if target == 'ios-unsigned' else 'iPhoneSimulator'
        if (info.get('CFBundleSupportedPlatforms') != [expected_platform]
                or (target == 'ios-unsigned' and info.get('DTPlatformName') != 'iphoneos')):
            raise ValueError('application platform does not match requested target')
        profile = contents / 'embedded.mobileprovision'
        if target == 'ios-unsigned' and (profile.exists() or profile.is_symlink()):
            raise ValueError('unsigned device application must not contain a provisioning profile')
        for key in ['CFBundleIdentifier', 'CFBundleShortVersionString', 'CFBundleVersion']:
            if not isinstance(info.get(key), str) or not info[key].strip():
                raise ValueError('missing bundle identity or version')
        name = info.get('CFBundleExecutable')
        if not isinstance(name, str) or name in ('', '.', '..') or '/' in name or '\\' in name:
            raise ValueError('unsafe application executable name')
        executable = (contents / 'MacOS' if macos else contents) / name
        if executable.is_symlink() or not executable.is_file() or not executable.stat().st_mode & 0o111:
            raise ValueError('missing regular application executable')
    except (OSError, plistlib.InvalidFileException, AttributeError) as error:
        raise ValueError('invalid application bundle metadata') from error
    archs = native(['lipo', '-archs', str(executable)], capture=True).stdout.split()
    if not archs or len(set(archs)) != len(archs) or not set(archs) <= {'arm64', 'x86_64'}:
        raise ValueError('unexpected application executable architecture')
    if target == 'ios-unsigned' and archs != ['arm64']:
        raise ValueError('unsigned iOS device application requires only arm64')
    signature = native(['codesign', '-dv', '--verbose=4', str(bundle)], capture=True, check=False)
    if (signature.returncode != 1 or 'code object is not signed at all' not in signature.stderr
            or (contents / '_CodeSignature').exists()):
        raise ValueError('expected unsigned application; signature inspection did not confirm it')
    return entries, {
        'bundleIdentifier': info['CFBundleIdentifier'], 'version': info['CFBundleShortVersionString'],
        'buildNumber': info['CFBundleVersion'], 'platform': expected_platform,
        'architectures': sorted(archs), 'bundle': bundle.name,
        'signatureScope': 'top-level application bundle; embedded framework signatures are not certified',
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--application', type=Path, required=True)
    parser.add_argument('--target', choices=['web', 'web-preview', 'android-debug', 'android-unsigned-apk', 'android-unsigned-aab', 'ios-simulator', 'ios-unsigned', 'macos-unsigned', 'windows-unsigned-msix'], required=True)
    parser.add_argument('--api-url')
    parser.add_argument('--preview-arch', choices=['arm64', 'amd64'])
    parser.add_argument('--go', default='go', help='Go compiler for the isolated preview backend')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--flutter', default='flutter')
    parser.add_argument('--xcrun', default='xcrun', help='Apple tool locator; used only for Apple diagnostic targets')
    parser.add_argument('--aapt', help='Administrator-owned Android metadata tool for unsigned release builds')
    parser.add_argument('--apksigner', help='Administrator-owned Android signature verifier for unsigned release builds')
    parser.add_argument('--java-home', help='Administrator-owned JDK for AAB verification')
    parser.add_argument('--bundletool', help='Absolute administrator-owned bundletool JAR')
    parser.add_argument('--bundletool-sha256', help='Approved bundletool SHA256')
    parser.add_argument('--bundle-verifier', help='Absolute administrator-owned all-entry JAR verifier source')
    parser.add_argument('--makeappx', help='Absolute administrator-owned Windows SDK MakeAppx executable')
    parser.add_argument('--windows-publisher', help='Exact certificate Subject/MSIX Publisher')
    parser.add_argument('--windows-package', help='Approved MSIX Identity Name')
    parser.add_argument('--windows-version', help='Approved four-component MSIX package version')
    parser.add_argument('--windows-assets', type=Path, help='Local exact-size StoreLogo/Square150x150Logo/Square44x44Logo PNG directory')
    parser.add_argument('--local-testing', action='store_true')
    args = parser.parse_args()
    preview = args.target == 'web-preview'
    if preview:
        if args.api_url or not args.preview_arch or args.local_testing:
            parser.error('web-preview requires --preview-arch; API URL and local-testing are not accepted')
        args.api_url = '@preview'
    elif not args.api_url:
        parser.error('--api-url is required for this target')
    try:
        url = urlsplit(args.api_url)
        valid = (url.scheme in ('http', 'https') and url.hostname and url.port != 0
                 and not url.username and not url.password and not url.query and not url.fragment)
        local = url.hostname == 'localhost'
        try:
            local = local or ipaddress.ip_address(url.hostname or '').is_loopback
        except ValueError:
            pass
        local = local or (args.target == 'android-debug' and url.hostname == '10.0.2.2')
        if not preview and (not valid or (url.scheme == 'http' and (args.target in ('android-unsigned-apk', 'android-unsigned-aab', 'ios-unsigned', 'windows-unsigned-msix') or not (args.local_testing and local)))):
            parser.error('API URL must use HTTPS without credentials/query/fragment; explicit local testing permits loopback HTTP only')
    except ValueError:
        parser.error('invalid API URL')
    app = args.application.resolve()
    client = app / 'client'
    try:
        application = json.loads((app / 'application.json').read_text())
        if application.get('mode') not in ('feedback', 'booking'):
            raise ValueError('unsupported application mode')
        if not (client / 'lib/main.dart').is_file() or not (client / 'pubspec.yaml').is_file():
            raise ValueError('missing Flutter sources')
    except (OSError, ValueError) as error:
        parser.error('invalid generated application: ' + str(error))
    android_tools = None
    aab_tools = None
    windows = args.target == 'windows-unsigned-msix'
    if windows:
        if sys.platform != 'win32':
            parser.error('MSIX builds require a native Windows execution node')
        if (not windows_msix.valid_publisher(args.windows_publisher) or not args.windows_package
                or not windows_msix.PACKAGE.fullmatch(args.windows_package) or not windows_msix.valid_version(args.windows_version)
                or not args.makeappx or not Path(args.makeappx).is_absolute() or not shutil.which(args.makeappx)
                or not args.windows_assets or args.windows_assets.is_symlink() or not args.windows_assets.is_dir()):
            parser.error('explicit approved Windows identity, SDK tool and local logo directory required')
        for name, size in windows_msix.LOGOS.items():
            path = args.windows_assets / name
            if path.is_symlink() or not path.is_file():
                parser.error('missing exact-size Windows logo')
            windows_msix.png(path.read_bytes(), size)
    if args.target in ('android-unsigned-apk', 'android-unsigned-aab'):
        if application.get('androidUnsignedRelease') is not True:
            parser.error('unsigned release requires explicit --android-unsigned-release generation')
    if args.target == 'android-unsigned-aab':
        aab_tools = {'javaHome': args.java_home, 'bundletoolPath': args.bundletool,
                     'bundletoolSHA256': args.bundletool_sha256, 'bundleVerifierPath': args.bundle_verifier,
                     'bundleId': application.get('organization', '') + '.' + application['name']}
        android_bundle.configuration(aab_tools)
    if args.target == 'android-unsigned-apk':
        android_tools = {}
        for tool in ['aapt', 'apksigner']:
            name = getattr(args, tool)
            if not name or not Path(name).is_absolute() or not shutil.which(name):
                parser.error('unsigned release requires explicit absolute administrator Android SDK tool paths')
            android_tools[tool] = name
    output = args.output.absolute()
    metadata = Path(str(output) + '.json')
    if output.exists() or output.is_symlink() or metadata.exists() or metadata.is_symlink():
        parser.error('artifact or manifest already exists; refusing to overwrite')
    flutter = shutil.which(args.flutter)
    if not flutter:
        parser.error('Flutter executable was not found')
    go = shutil.which(args.go) if preview else None
    if preview and (not go or not (app / 'backend/go.mod').is_file() and not (app / 'backend/main.go').is_file()):
        parser.error('web-preview requires Go and generated backend sources')
    environment = {**os.environ, 'CI': 'true', 'FLUTTER_SUPPRESS_ANALYTICS': 'true', 'DART_SUPPRESS_ANALYTICS': 'true'}
    flutter_argv = [flutter]
    windows_sdk_snapshot = None
    if windows and Path(flutter).suffix.lower() in ('.bat', '.cmd'):
        # Bypass the batch command interpreter so approved URLs/paths remain
        # literal arguments. Administrators prepare the Flutter SDK cache first.
        flutter_bin = Path(flutter).parent
        dart = flutter_bin / 'cache/dart-sdk/bin/dart.exe'
        packages = flutter_bin.parent / 'packages/flutter_tools/.dart_tool/package_config.json'
        windows_sdk_snapshot = flutter_bin / 'cache/flutter_tools.snapshot'
        if any(p.is_symlink() or not p.is_file() for p in (dart, packages, windows_sdk_snapshot)):
            parser.error('Windows Flutter SDK requires a pre-provisioned Dart executable, package configuration and tool snapshot')
        flutter_argv = [str(dart), '--disable-dart-dev', '--packages=' + str(packages), str(windows_sdk_snapshot)]
        environment['FLUTTER_ROOT'] = str(flutter_bin.parent)
    commands = []
    native_commands = []

    def native(command, capture=False, check=True):
        native_commands.append(command)
        result = subprocess.run([xcrun, *command], cwd=client,
                                env={**environment, 'LC_ALL': 'C', 'LANG': 'C'},
                                text=True, capture_output=capture)
        if check and result.returncode:
            if capture:
                sys.stderr.write(result.stderr)
            raise subprocess.CalledProcessError(result.returncode, result.args)
        return result

    apple = None
    if args.target in ('ios-simulator', 'ios-unsigned', 'macos-unsigned'):
        xcrun = shutil.which(args.xcrun)
        if not xcrun:
            parser.error('xcrun was not found; Apple builds require an installed Xcode')
        # Fail on an unaccepted license before running Flutter or changing caches.
        xcode = native(['xcodebuild', '-version'], capture=True).stdout.strip()
        sdk_name = 'macosx' if args.target == 'macos-unsigned' else 'iphoneos' if args.target == 'ios-unsigned' else 'iphonesimulator'
        sdk_version = native(['--sdk', sdk_name, '--show-sdk-version'], capture=True).stdout.strip()
        if not xcode.startswith('Xcode ') or not sdk_version:
            raise ValueError('Xcode or Apple SDK version was not reported')
        apple = {'xcodeVersion': xcode, 'sdk': {'name': sdk_name, 'version': sdk_version},
                 'xcrunExecutable': xcrun, 'developerDirectory': os.environ.get('DEVELOPER_DIR')}

    def run(command, capture=False):
        commands.append(command)
        result = subprocess.run([*flutter_argv, *command], cwd=client, env=environment,
                                check=True, text=True, capture_output=capture)
        return result.stdout if capture else None

    sdk = json.loads(run(['--version', '--machine'], capture=True))
    run(['pub', 'get'])
    run(['analyze', '--no-pub'])
    run(['test', '--no-pub'])
    target = {
        'web': ['web', '--release', '--no-web-resources-cdn'],
        'web-preview': ['web', '--release', '--no-web-resources-cdn'],
        'android-debug': ['apk', '--debug', '--target-platform=android-arm64'],
        'android-unsigned-apk': ['apk', '--release', '--target-platform=android-arm64'],
        'android-unsigned-aab': ['appbundle', '--release', '--target-platform=android-arm64'],
        'ios-simulator': ['ios', '--debug', '--simulator', '--no-codesign'],
        'ios-unsigned': ['ios', '--release', '--no-codesign'],
        'macos-unsigned': ['macos', '--release', '--config-only'],
        'windows-unsigned-msix': ['windows', '--release', '--target-platform=windows-x64'],
    }[args.target]
    run(['build', *target, '--no-pub', '--dart-define=API_BASE_URL=' + args.api_url.rstrip('/'),
         '--dart-define=APP_MODE=' + application['mode']])
    android = None
    windows_info = None
    windows_body = None
    if windows:
        with tempfile.TemporaryDirectory(prefix='reference-msix-') as directory:
            work = Path(directory)
            windows_msix.create_layout(client / 'build/windows/x64/runner/Release', work / 'layout',
                package=args.windows_package, publisher=args.windows_publisher, version=args.windows_version,
                executable=application['name'] + '.exe', logo=args.windows_assets, display_name=application['name'])
            package = work / 'unsigned.msix'
            command = [args.makeappx, 'pack', '/h', 'SHA256', '/d', str(work / 'layout'), '/p', str(package), '/no']
            native_commands.append(command)
            subprocess.run(command, env=environment, check=True, timeout=600)
            windows_info = windows_msix.inspect(package)
            windows_info['makeAppxSHA256'] = sha256(Path(args.makeappx))
            if windows_sdk_snapshot:
                windows_info['flutterToolSnapshotSHA256'] = sha256(windows_sdk_snapshot)
            windows_body = package.read_bytes()
    if aab_tools:
        aab = client / 'build/app/outputs/bundle/release/app-release.aab'
        with tempfile.TemporaryDirectory(prefix='aab-verification-') as directory:
            android = android_bundle.inspect(aab_tools, directory, aab)
        android.update({'debuggable': False, 'signatureVerified': False, 'unsignedStateInspected': True,
                        'bundletoolSHA256': args.bundletool_sha256,
                        'payloadDigest': android_bundle.archive(aab)['payloadDigest']})
    if android_tools:
        apk = client / 'build/app/outputs/flutter-apk/app-release.apk'
        if apk.is_symlink() or not apk.is_file():
            raise ValueError('unsigned release APK was not produced as a regular file')
        def inspect_sdk(tool, *options):
            native_commands.append([android_tools[tool], *map(str, options)])
            return subprocess.run([android_tools[tool], *map(str, options)],
                env={**environment, 'LC_ALL': 'C', 'LANG': 'C'}, capture_output=True, text=True, timeout=60)
        inspected = inspect_sdk('aapt', 'dump', 'badging', apk)
        expected_package = application.get('organization', '') + '.' + application['name']
        packages = re.findall(r"^package: name='([^']+)' versionCode='([0-9]+)' versionName='([^']+)'", inspected.stdout, re.M)
        minimum = re.findall(r"^sdkVersion:'([0-9]+)'", inspected.stdout, re.M)
        if (inspected.returncode or len(packages) != 1 or packages[0][0] != expected_package
                or not 1 <= int(packages[0][1]) <= 2100000000 or not minimum or int(minimum[0]) < 24
                or 'application-debuggable' in inspected.stdout or "native-code: 'arm64-v8a'" not in inspected.stdout):
            raise ValueError('unsigned release APK identity, ABI, SDK or debuggable state is invalid')
        signature = inspect_sdk('apksigner', 'verify', '--verbose', apk)
        if signature.returncode != 1 or 'Missing META-INF/MANIFEST.MF' not in signature.stderr:
            raise ValueError('unsigned release APK has a signature or its unsigned state could not be verified')
        android = {'packageName': packages[0][0], 'versionCode': int(packages[0][1]),
                   'versionName': packages[0][2], 'minSDK': int(minimum[0]), 'debuggable': False,
                   'signatureVerified': False, 'unsignedStateInspected': True}
    bundle = None
    if args.target == 'ios-simulator':
        bundle = client / 'build/ios/iphonesimulator/Runner.app'
    elif args.target == 'ios-unsigned':
        bundle = client / 'build/ios/iphoneos/Runner.app'
    elif args.target == 'macos-unsigned':
        build_root = client / 'build'
        build_root.mkdir(exist_ok=True)
        derived = Path(tempfile.mkdtemp(prefix='reference-macos-', dir=build_root))
        native(['xcodebuild', '-workspace', str(client / 'macos/Runner.xcworkspace'),
                '-scheme', 'Runner', '-configuration', 'Release',
                '-destination', 'generic/platform=macOS', '-derivedDataPath', str(derived),
                'CODE_SIGNING_ALLOWED=NO', 'CODE_SIGNING_REQUIRED=NO', 'CODE_SIGN_IDENTITY=', 'build'])
        bundles = sorted((derived / 'Build/Products/Release').glob('*.app'))
        if len(bundles) != 1:
            raise ValueError('expected exactly one macOS application product')
        bundle = bundles[0]
    if bundle is not None:
        entries, identity = inspect_apple(bundle, args.target, native)
        apple.update(identity)
    preview_backend = None
    if preview:
        preview_backend = client / 'build/preview/backend'
        preview_backend.parent.mkdir(parents=True, exist_ok=True)
        command = [go, 'build', '-trimpath', '-ldflags=-s -w', '-o', str(preview_backend), '.']
        native_commands.append(command)
        subprocess.run(command, cwd=app / 'backend', env={**environment, 'GOOS': 'linux',
                       'GOARCH': args.preview_arch, 'CGO_ENABLED': '0'}, check=True)
    # Hash inputs, including locked dependencies and generated platform sources,
    # while excluding caches, local SDK paths and build outputs.
    ignored = {'.git', '.dart_tool', '.gradle', 'build', 'Pods', 'ephemeral', '.symlinks'}
    inputs = {}
    for path in sorted(client.rglob('*')):
        relative = path.relative_to(client)
        if any(part in ignored for part in relative.parts) or path.name in ('local.properties', '.flutter-plugins-dependencies'):
            continue
        if path.is_file() and not path.is_symlink():
            inputs[relative.as_posix()] = sha256(path)
    source_digest = hashlib.sha256(json.dumps(inputs, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    output.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(prefix='.reference-artifact-', dir=output.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, 'wb') as stream:
            if args.target in ('web', 'web-preview'):
                built = client / 'build/web'
                if not (built / 'index.html').is_file():
                    raise ValueError('Flutter did not produce a Web entry point')
                with tarfile.open(fileobj=stream, mode='w:gz', format=tarfile.PAX_FORMAT) as archive:
                    if preview:
                        for name, body in reference_preview.entries(built, preview_backend, application['mode'], args.preview_arch):
                            member = tarfile.TarInfo(name); member.mode = 0o644; member.size = len(body)
                            archive.addfile(member, io.BytesIO(body))
                    else:
                        for path in sorted(built.rglob('*')):
                            if path.is_symlink() or not (path.is_file() or path.is_dir()):
                                raise ValueError('unsafe entry in Web build')
                            archive.add(path, arcname=path.relative_to(built).as_posix(), recursive=False)
            elif args.target in ('android-debug', 'android-unsigned-apk'):
                apk_name = 'app-debug.apk' if args.target == 'android-debug' else 'app-release.apk'
                with (client / 'build/app/outputs/flutter-apk' / apk_name).open('rb') as package:
                    shutil.copyfileobj(package, stream)
            elif args.target == 'android-unsigned-aab':
                with aab.open('rb') as package:
                    shutil.copyfileobj(package, stream)
            elif windows:
                stream.write(windows_body)
            else:
                with tarfile.open(fileobj=stream, mode='w:gz', format=tarfile.PAX_FORMAT,
                                  dereference=False) as archive:
                    for path in entries:
                        archive.add(path, arcname=path.relative_to(bundle.parent).as_posix(), recursive=False)
        manifest = {
            'schemaVersion': 1, 'builtAt': datetime.now(timezone.utc).isoformat(),
            'name': application['name'], 'mode': application['mode'], 'target': args.target,
            'apiBaseURL': args.api_url.rstrip('/'), 'localTesting': args.local_testing,
            'sdk': sdk, 'commands': commands, 'sourceDigest': source_digest, 'sourceFiles': inputs,
            'artifact': {'file': output.name, 'sha256': sha256(temporary), 'bytes': temporary.stat().st_size},
            'signing': ('unsigned-msix' if windows else 'unsigned-aab' if aab_tools else 'unsigned-apk' if android else 'unsigned-application-bundle' if apple else
                        'development-debug-key' if args.target == 'android-debug' else 'not-applicable'),
            'architecture': ('windows-x64' if windows else ','.join(apple['architectures']) if apple else
                             'android-arm64' if args.target in ('android-debug', 'android-unsigned-apk', 'android-unsigned-aab') else 'web-javascript'),
            'validation': {'analyze': True, 'widgetTests': True, 'browser': False, 'emulator': False,
                           'nativeApplication': False, 'physicalDevice': False, 'storePublished': False},
        }
        if apple:
            manifest['apple'] = apple
            manifest['nativeCommands'] = native_commands
        if preview:
            manifest['preview'] = {'profile': 'flutter-go-v1', 'goarch': args.preview_arch,
                                   'backendSHA256': sha256(preview_backend), 'backendBytes': preview_backend.stat().st_size,
                                   'temporaryData': True}
            manifest['backendSourceFiles'] = {p.relative_to(app / 'backend').as_posix(): sha256(p)
                for p in sorted((app / 'backend').rglob('*')) if p.is_file() and not p.is_symlink()}
            manifest['nativeCommands'] = native_commands
        if android:
            manifest['android'] = android
            manifest['nativeCommands'] = native_commands
        if windows:
            manifest['windows'] = windows_info
            manifest['nativeCommands'] = native_commands
        # Stage both files before publishing; never replace a concurrent writer.
        descriptor, metadata_name = tempfile.mkstemp(prefix='.reference-manifest-', dir=output.parent)
        temporary_metadata = Path(metadata_name)
        owned_output = False
        try:
            with os.fdopen(descriptor, 'w') as stream:
                json.dump(manifest, stream, ensure_ascii=False, indent=2)
                stream.write('\n')
            os.link(temporary, output)
            owned_output = True
            os.link(temporary_metadata, metadata)
        except OSError:
            if owned_output and not output.is_symlink() and output.samefile(temporary):
                output.unlink()
            raise
        finally:
            temporary_metadata.unlink(missing_ok=True)
    finally:
        temporary.unlink(missing_ok=True)
    print(json.dumps({'artifact': str(output), 'manifest': str(metadata), 'sha256': manifest['artifact']['sha256']}))
    return 0


if __name__ == '__main__':
    try:
        sys.exit(main())
    except subprocess.CalledProcessError as error:
        print('Build command failed; no artifact was published.', file=sys.stderr)
        sys.exit(error.returncode if error.returncode > 0 else 1)
    except (OSError, ValueError) as error:
        print('Build failed: ' + str(error), file=sys.stderr)
        sys.exit(1)
