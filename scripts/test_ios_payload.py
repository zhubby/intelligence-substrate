import copy
import io
from pathlib import Path
import plistlib
import struct
import tarfile
import tempfile
import unittest

import ios_payload


def macho(platform=2, cpu=0x100000c, kind=2):
    # Deliberately structural Mach-O fixture, not executable application proof.
    return struct.pack('<8I', 0xfeedfacf, cpu, 0, kind, 1, 24, 0, 0) + struct.pack('<6I', 0x32, 24, platform, 0xf0000, 0x1b0000, 0)


def app_entries():
    return {
        'Runner.app/Info.plist': (plistlib.dumps({'CFBundlePackageType':'APPL', 'CFBundleIdentifier':'com.example.app',
            'CFBundleExecutable':'Runner','CFBundleShortVersionString':'1.2.3','CFBundleVersion':'9',
            'CFBundleSupportedPlatforms':['iPhoneOS'],'DTPlatformName':'iphoneos'}), 0o644),
        'Runner.app/Runner': (macho(), 0o755),
        'Runner.app/Frameworks/Flutter.framework/Flutter': (macho(kind=6), 0o755),
        'Runner.app/Frameworks/Flutter.framework/Info.plist': (plistlib.dumps({'CFBundlePackageType':'FMWK'}), 0o644),
        'Runner.app/assets/content.txt': (b'approved application resource', 0o644),
    }


def archive(path, entries):
    with tarfile.open(path, 'w:gz') as package:
        for name, (body, mode) in entries.items():
            member = tarfile.TarInfo(name); member.mode = mode; member.size = len(body)
            package.addfile(member, io.BytesIO(body))


class IOSPayloadTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(); self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def inspect(self, entries):
        source = self.root/'unsigned.tar.gz'; archive(source, entries)
        return ios_payload.inspect(source, self.root/'extracted')

    def test_exact_flat_device_app_and_framework_are_preserved(self):
        metadata = self.inspect(app_entries())
        self.assertEqual(metadata['bundleId'], 'com.example.app')
        self.assertEqual(metadata['version'], '1.2.3')
        self.assertEqual(metadata['build'], '9')
        self.assertEqual(Path(metadata['app'],'assets/content.txt').read_bytes(), b'approved application resource')
        self.assertEqual(metadata['code'], ['Runner.app/Runner', 'Runner.app/Frameworks/Flutter.framework/Flutter'])

    def test_macos_simulator_foreign_arch_and_truncated_load_commands_are_rejected(self):
        cases = [macho(platform=1), macho(platform=7), macho(cpu=0x1000007), macho(kind=1), macho()[:40],
                 macho()[:32]+struct.pack('<6I',0x32,100,2,0,0,0), b'#!/bin/sh\necho nope\n']
        for index, body in enumerate(cases):
            with self.subTest(index=index), tempfile.TemporaryDirectory() as directory:
                entries=app_entries(); entries['Runner.app/Runner']=(body,0o755)
                source=Path(directory)/'app.tar.gz'; archive(source, entries)
                with self.assertRaises(ValueError): ios_payload.inspect(source,Path(directory)/'app')

    def test_profile_signature_metadata_and_nested_app_are_not_unsigned_inputs(self):
        for path in ['Runner.app/embedded.mobileprovision', 'Runner.app/_CodeSignature/CodeResources',
                     'Runner.app/PlugIns/Extension.appex/Info.plist', 'Runner.app/Watch/Watch.app/Info.plist']:
            with self.subTest(path=path), tempfile.TemporaryDirectory() as directory:
                entries=app_entries(); entries[path]=(b'foreign signing resource',0o644)
                source=Path(directory)/'app.tar.gz'; archive(source,entries)
                with self.assertRaises(ValueError): ios_payload.inspect(source,Path(directory)/'app')

    def test_device_info_and_framework_platform_cannot_be_faked(self):
        for mode in ['simulator-info','wrong-dt-platform','framework']:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                entries=app_entries()
                if mode=='framework': entries['Runner.app/Frameworks/Flutter.framework/Flutter']=(macho(platform=7,kind=6),0o755)
                else:
                    info=plistlib.loads(entries['Runner.app/Info.plist'][0])
                    info['CFBundleSupportedPlatforms' if mode=='simulator-info' else 'DTPlatformName']=['iPhoneSimulator'] if mode=='simulator-info' else 'iphonesimulator'
                    entries['Runner.app/Info.plist']=(plistlib.dumps(info),0o644)
                source=Path(directory)/'app.tar.gz'; archive(source,entries)
                with self.assertRaises(ValueError): ios_payload.inspect(source,Path(directory)/'app')


if __name__ == '__main__': unittest.main()
