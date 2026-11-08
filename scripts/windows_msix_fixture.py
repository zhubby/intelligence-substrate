"""Test-only structural MSIX fixtures; these are not Windows-installable evidence."""
import base64
import hashlib
from pathlib import Path
import struct
import zipfile
import zlib
from xml.sax.saxutils import escape


def pe(dll=False):
    body=bytearray(1024);body[:2]=b'MZ';struct.pack_into('<I',body,60,128);body[128:132]=b'PE\0\0'
    struct.pack_into('<HHIIIHH',body,132,0x8664,1,0,0,0,240,0x2022 if dll else 0x22)
    struct.pack_into('<H',body,152,0x20b);struct.pack_into('<I',body,168,0x1000)
    struct.pack_into('<H',body,220,2);struct.pack_into('<I',body,260,16)
    body[392:400]=b'.text\0\0\0';struct.pack_into('<IIII',body,400,512,0x1000,512,512)
    body[512]=0xc3
    return bytes(body)


def png(size):
    def chunk(kind,body):
        return struct.pack('>I',len(body))+kind+body+struct.pack('>I',zlib.crc32(kind+body)&0xffffffff)
    return b'\x89PNG\r\n\x1a\n'+chunk(b'IHDR',struct.pack('>II',size,size)+b'\x08\x06\0\0\0')+chunk(b'IDAT',zlib.compress((b'\0'+b'\x18\x38\x78\xff'*size)*size))+chunk(b'IEND',b'')


def entries(publisher='CN=Company',name='com.company.feedback',version='1.0.0.0'):
    manifest=f'''<?xml version="1.0" encoding="utf-8"?>
<Package xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10" xmlns:uap="http://schemas.microsoft.com/appx/manifest/uap/windows10" xmlns:rescap="http://schemas.microsoft.com/appx/manifest/foundation/windows10/restrictedcapabilities" IgnorableNamespaces="uap rescap">
<Identity Name="{escape(name)}" Publisher="{escape(publisher)}" Version="{version}" ProcessorArchitecture="x64"/>
<Properties><DisplayName>Feedback</DisplayName><PublisherDisplayName>Company</PublisherDisplayName><Logo>Assets/StoreLogo.png</Logo></Properties>
<Dependencies><TargetDeviceFamily Name="Windows.Desktop" MinVersion="10.0.17763.0" MaxVersionTested="10.0.26100.0"/></Dependencies>
<Resources><Resource Language="en-US"/></Resources>
<Applications><Application Id="App" Executable="app/feedback.exe" EntryPoint="Windows.FullTrustApplication"><uap:VisualElements DisplayName="Feedback" Description="Feedback" Square150x150Logo="Assets/Square150x150Logo.png" Square44x44Logo="Assets/Square44x44Logo.png" BackgroundColor="transparent"/></Application></Applications>
<Capabilities><Capability Name="internetClient"/><rescap:Capability Name="runFullTrust"/></Capabilities></Package>'''.encode()
    return {'AppxManifest.xml':manifest,'app/feedback.exe':pe(),'app/flutter_windows.dll':pe(True),
            'app/data/flutter_assets/content.txt':b'approved Windows resource',
            'Assets/StoreLogo.png':png(50),'Assets/Square150x150Logo.png':png(150),'Assets/Square44x44Logo.png':png(44)}


def write(path,values=None,signed=False,blockmap_change=None,content_types=None):
    values=dict(entries() if values is None else values)
    blockmap=['<BlockMap xmlns="http://schemas.microsoft.com/appx/2010/blockmap" HashMethod="http://www.w3.org/2001/04/xmlenc#sha256">']
    for name,body in values.items():
        blockmap.append(f'<File Name="{escape(name.replace(chr(47),chr(92)))}" Size="{len(body)}" LfhSize="{30+len(name.encode())}">')
        for offset in range(0,len(body),65536):
            blockmap.append('<Block Hash="'+base64.b64encode(hashlib.sha256(body[offset:offset+65536]).digest()).decode()+'"/>')
        blockmap.append('</File>')
    blockmap.append('</BlockMap>');bm=''.join(blockmap).encode()
    if blockmap_change: bm=blockmap_change(bm)
    values['AppxBlockMap.xml']=bm
    values['[Content_Types].xml']=content_types or b'<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="exe" ContentType="application/octet-stream"/><Default Extension="dll" ContentType="application/octet-stream"/><Default Extension="png" ContentType="image/png"/><Default Extension="txt" ContentType="text/plain"/><Default Extension="p7x" ContentType="application/vnd.ms-appx.signature"/><Override PartName="/AppxManifest.xml" ContentType="application/vnd.ms-appx.manifest+xml"/><Override PartName="/AppxBlockMap.xml" ContentType="application/vnd.ms-appx.blockmap+xml"/></Types>'
    if signed: values['AppxSignature.p7x']=b'PKCXsynthetic signature provider only'
    with zipfile.ZipFile(path,'w',zipfile.ZIP_DEFLATED) as archive:
        for name,body in values.items(): archive.writestr(name,body)
    Path(path).chmod(0o600)
    return path
