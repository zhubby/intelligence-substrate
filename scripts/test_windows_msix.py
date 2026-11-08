import base64
import hashlib
from pathlib import Path
import struct
import tempfile
import unittest
import zipfile

import windows_msix_fixture as fixture
import windows_msix


class MSIXTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name)

    def test_exact_x64_identity_and_full_blockmap(self):
        path=fixture.write(self.root/'app.msix');info=windows_msix.inspect(path)
        self.assertEqual((info['packageName'],info['publisher'],info['version'],info['architecture']),('com.company.feedback','CN=Company','1.0.0.0','x64'))
        self.assertEqual(info['members']['app/data/flutter_assets/content.txt'],hashlib.sha256(b'approved Windows resource').hexdigest())
        self.assertEqual(info['executable'],'app/feedback.exe')

    def test_corrupt_blocks_and_unmapped_payload_are_rejected(self):
        for mode in ['digest','missing','size','method','duplicate','foreign','empty']:
            with self.subTest(mode=mode):
                def change(body):
                    if mode=='digest': return body.replace(b'Hash="',b'Hash="A',1)
                    if mode=='missing':return body.replace(b'Name="app\\feedback.exe"',b'Name="app\\other.exe"')
                    if mode=='size':return body.replace(b'Size="1024"',b'Size="1025"',1)
                    if mode=='method':return body.replace(b'xmlenc#sha256',b'xmlenc#sha512')
                    if mode=='duplicate':return body.replace(b'</BlockMap>',body[body.index(b'<File'):body.index(b'</File>')+7]+b'</BlockMap>')
                    if mode=='foreign':return body.replace(b'<File ',b'<Other ',1)
                    return body.replace(b'<BlockMap ',b'<BlockMap xmlns:x="urn:x" ',1).replace(b'</BlockMap>',b'<x:Hidden/></BlockMap>')
                with self.assertRaises(ValueError):windows_msix.inspect(fixture.write(self.root/(mode+'.msix'),blockmap_change=change))

    def test_windows_path_collisions_escapes_and_devices_are_rejected(self):
        for name in ['../escape','app/../escape','app\\escape','app/C:secret','app/CON','app/aux.txt','app/NUL.dll','app/trailing.','app/trailing ','/absolute','app//double','app/feedback.exe/child','APP/Feedback.exe']:
            with self.subTest(name=name):
                values=fixture.entries();values[name]=b'x'
                with self.assertRaises(ValueError):windows_msix.inspect(fixture.write(self.root/'bad.msix',values))

    def test_xml_extensions_capabilities_identity_and_pe_are_rejected(self):
        changes={
          'entities':lambda b:b.replace(b'<Package ',b'<!DOCTYPE Package [<!ENTITY x "x">]><Package ',1),
          'arm64':lambda b:b.replace(b'ProcessorArchitecture="x64"',b'ProcessorArchitecture="arm64"'),
          'version':lambda b:b.replace(b'Version="1.0.0.0"',b'Version="1.0.0.65536"'),
          'publisher':lambda b:b.replace(b'Publisher="CN=Company"',b'Publisher=""'),
          'extensions':lambda b:b.replace(b'</Application>',b'<Extensions/></Application>'),
          'capability':lambda b:b.replace(b'Name="internetClient"',b'Name="broadFileSystemAccess"'),
          'resource':lambda b:b.replace(b'Square44x44Logo.png',b'Missing.png'),
          'multiple':lambda b:b.replace(b'</Applications>',b'<Application Id="Other"/></Applications>'),
        }
        for name,change in changes.items():
            with self.subTest(name=name):
                values=fixture.entries();values['AppxManifest.xml']=change(values['AppxManifest.xml'])
                with self.assertRaises(ValueError):windows_msix.inspect(fixture.write(self.root/'bad.msix',values))
        for offset,data in [(0,b'NO'),(132,b'\x64\xaa'),(152,b'\x0b\x01'),(220,b'\x03\0'),(60,b'\xff'*4)]:
            values=fixture.entries();pe=bytearray(values['app/feedback.exe']);pe[offset:offset+len(data)]=data;values['app/feedback.exe']=bytes(pe)
            with self.assertRaises(ValueError):windows_msix.inspect(fixture.write(self.root/'bad-pe.msix',values))

    def test_signature_state_and_content_integrity_are_distinct(self):
        signed=fixture.write(self.root/'signed.msix',signed=True)
        with self.assertRaises(ValueError):windows_msix.inspect(signed)
        info=windows_msix.inspect(signed,signed=True)
        plain=fixture.write(self.root/'plain.msix');unsigned=windows_msix.inspect(plain)
        self.assertEqual(info['payloadDigest'],unsigned['payloadDigest'])
        windows_msix.same_payload(unsigned,info)
        values=fixture.entries();values['app/data/flutter_assets/content.txt']=b'changed approved bytes'
        changed=windows_msix.inspect(fixture.write(self.root/'changed.msix',values,signed=True),signed=True)
        with self.assertRaises(ValueError):windows_msix.same_payload(unsigned,changed)
        with self.assertRaises(ValueError):windows_msix.inspect(plain,signed=True)

    def test_duplicate_zip_special_entries_and_limit_are_rejected(self):
        p=fixture.write(self.root/'duplicate.msix')
        with zipfile.ZipFile(p,'a') as z:z.writestr('app/FEEDBACK.exe',b'x')
        with self.assertRaises(ValueError):windows_msix.inspect(p)
        p=fixture.write(self.root/'link.msix')
        with zipfile.ZipFile(p,'a') as z:
            info=zipfile.ZipInfo('app/link');info.external_attr=0o120777<<16;z.writestr(info,b'feedback.exe')
        with self.assertRaises(ValueError):windows_msix.inspect(p)
        p=fixture.write(self.root/'huge.msix',{**fixture.entries(),'app/huge':b'A'*(2<<20)})
        with self.assertRaises(ValueError):windows_msix.inspect(p,max_expanded=1<<20)

    def test_controlled_layout_copies_exact_binary_and_escapes_publisher(self):
        release=self.root/'release';release.mkdir();(release/'feedback.exe').write_bytes(fixture.pe());(release/'flutter_windows.dll').write_bytes(fixture.pe(True))
        logo=self.root/'logos';logo.mkdir()
        for name,body in fixture.entries().items():
            if name.startswith('Assets/'): (logo/Path(name).name).write_bytes(body)
        layout=self.root/'layout'
        windows_msix.create_layout(release,layout,package='com.company.feedback',publisher='CN=Company & Friends',version='1.0.0.0',executable='feedback.exe',logo=logo,display_name='Feedback')
        self.assertEqual((layout/'app/feedback.exe').read_bytes(),fixture.pe())
        self.assertIn(b'CN=Company &amp; Friends',(layout/'AppxManifest.xml').read_bytes())
        self.assertTrue((layout/'Assets/Square44x44Logo.png').is_file())
        with self.assertRaises((ValueError,FileExistsError)):windows_msix.create_layout(release,layout,package='com.company.feedback',publisher='CN=Company',version='1.0.0.0',executable='feedback.exe',logo=logo,display_name='Feedback')


if __name__=='__main__':unittest.main()
