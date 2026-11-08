"""Bounded Windows MSIX content checks; OS signature trust is checked separately."""
import base64
import hashlib
import json
from pathlib import Path
import re
import shutil
import stat
import struct
import xml.etree.ElementTree as ET
from xml.sax.saxutils import escape, quoteattr
import zipfile

MAX_ARCHIVE=256<<20
MAX_EXPANDED=512<<20
FOUNDATION='http://schemas.microsoft.com/appx/manifest/foundation/windows10'
UAP='http://schemas.microsoft.com/appx/manifest/uap/windows10'
RESTRICTED='http://schemas.microsoft.com/appx/manifest/foundation/windows10/restrictedcapabilities'
BLOCKS='http://schemas.microsoft.com/appx/2010/blockmap'
TYPES='http://schemas.openxmlformats.org/package/2006/content-types'
SHA256='http://www.w3.org/2001/04/xmlenc#sha256'
PACKAGE=re.compile(r'[A-Za-z0-9][A-Za-z0-9.-]{1,48}[A-Za-z0-9]')
LOGOS={'StoreLogo.png':50,'Square150x150Logo.png':150,'Square44x44Logo.png':44}
SIGNATURE='AppxSignature.p7x'
FOOTPRINT={'AppxBlockMap.xml','[Content_Types].xml',SIGNATURE}


def digest(value):
    return hashlib.sha256(json.dumps(value,sort_keys=True,separators=(',',':'),ensure_ascii=True).encode()).hexdigest()


def valid_version(value):
    return (isinstance(value,str) and bool(re.fullmatch(r'(?:0|[1-9][0-9]{0,4})(?:\.(?:0|[1-9][0-9]{0,4})){3}',value))
            and all(int(v)<=65535 for v in value.split('.')) and int(value.split('.')[0])>0)


def valid_publisher(value):
    return isinstance(value,str) and value.startswith('CN=') and 4<=len(value)<=1024 and value==value.strip() and all(ord(c)>=32 and ord(c)!=127 for c in value)


def path_name(name):
    if not isinstance(name,str) or not 1<=len(name)<=240 or '\\' in name or not name.isascii(): raise ValueError('noncanonical Windows package path')
    parts=name.split('/')
    for part in parts:
        if (part in {'','.','..'} or part[-1] in ' .' or any(ord(c)<32 or c in '<>:"|?*' for c in part)
                or re.fullmatch(r'(CON|PRN|AUX|NUL|COM[0-9]|LPT[0-9])(?:\..*)?',part,re.I)):
            raise ValueError('unsafe Windows package path')
    return name


def xml(body,namespace,tag):
    if len(body)>1<<20 or b'<!DOCTYPE' in body.upper() or b'<!ENTITY' in body.upper() or b'\0' in body: raise ValueError('bounded entity-free UTF8 XML required')
    try:
        text=body.decode('utf-8-sig');root=ET.fromstring(text)
    except (UnicodeError,ET.ParseError) as error: raise ValueError('invalid package XML') from error
    if root.tag!='{'+namespace+'}'+tag or sum(1 for _ in root.iter())>20000: raise ValueError('unexpected XML root or element count')
    return root


def pe(body,dll=False):
    if len(body)<512 or body[:2]!=b'MZ': raise ValueError('native x64 PE executable required')
    offset=struct.unpack_from('<I',body,60)[0]
    if not 64<=offset<=1<<20 or offset+264>len(body) or body[offset:offset+4]!=b'PE\0\0': raise ValueError('invalid PE headers')
    machine,sections=struct.unpack_from('<HH',body,offset+4);optional_size,flags=struct.unpack_from('<HH',body,offset+20)
    opt=offset+24
    if (machine!=0x8664 or not 1<=sections<=96 or optional_size<240 or opt+optional_size+sections*40>len(body)
            or not flags&2 or bool(flags&0x2000)!=dll or struct.unpack_from('<H',body,opt)[0]!=0x20b
            or struct.unpack_from('<H',body,opt+68)[0]!=2 or struct.unpack_from('<I',body,opt+108)[0]<16):
        raise ValueError('supported x64 GUI PE32+ image required')
    entry=struct.unpack_from('<I',body,opt+16)[0]
    mapped=False
    for i in range(sections):
        at=opt+optional_size+i*40
        virtual_size,address,size,pointer=struct.unpack_from('<IIII',body,at+8)
        if size and (pointer<opt+optional_size+sections*40 or pointer+size>len(body)): raise ValueError('PE section outside image')
        if address<=entry<address+max(virtual_size,size) and pointer+max(0,entry-address)<len(body):mapped=True
    if not dll and (entry==0 or not mapped): raise ValueError('PE entry is not inside a mapped section')


def png(body,size):
    if (len(body)<33 or body[:8]!=b'\x89PNG\r\n\x1a\n' or body[12:16]!=b'IHDR'
            or struct.unpack_from('>II',body,16)!=(size,size) or len(body)>2<<20): raise ValueError('exact-size local PNG asset required')


def manifest(body,names):
    root=xml(body,FOUNDATION,'Package');ns='{'+FOUNDATION+'}'
    expected={'Identity','Properties','Dependencies','Resources','Applications','Capabilities'}
    if set(root.attrib)!={'IgnorableNamespaces'} or root.attrib['IgnorableNamespaces']!='uap rescap' or len(root)!=6 or {x.tag for x in root}!={ns+x for x in expected}: raise ValueError('unsupported package manifest structure')
    identity=root.find(ns+'Identity')
    if set(identity.attrib)!={'Name','Publisher','Version','ProcessorArchitecture'} or len(identity):raise ValueError('exact package identity required')
    data=identity.attrib
    if not PACKAGE.fullmatch(data['Name']) or not valid_publisher(data['Publisher']) or not valid_version(data['Version']) or data['ProcessorArchitecture']!='x64':raise ValueError('invalid approved Windows identity')
    props=root.find(ns+'Properties')
    if props.attrib or len(props)!=3 or {x.tag for x in props}!={ns+x for x in ['DisplayName','PublisherDisplayName','Logo']}:raise ValueError('unsupported package properties')
    for child in props:
        if child.attrib or len(child) or not child.text or len(child.text)>256:raise ValueError('bounded literal package properties required')
    if props.find(ns+'Logo').text!='Assets/StoreLogo.png':raise ValueError('fixed store logo required')
    dependencies=root.find(ns+'Dependencies')
    if dependencies.attrib or len(dependencies)!=1 or dependencies[0].tag!=ns+'TargetDeviceFamily' or dependencies[0].attrib!={'Name':'Windows.Desktop','MinVersion':'10.0.17763.0','MaxVersionTested':'10.0.26100.0'} or len(dependencies[0]):raise ValueError('supported desktop OS dependency required')
    resources=root.find(ns+'Resources')
    if resources.attrib or len(resources)!=1 or resources[0].tag!=ns+'Resource' or resources[0].attrib!={'Language':'en-US'} or len(resources[0]):raise ValueError('unsupported package resources')
    apps=root.find(ns+'Applications')
    if apps.attrib or len(apps)!=1 or apps[0].tag!=ns+'Application':raise ValueError('single full trust application required')
    app=apps[0];executable=app.get('Executable','').replace('\\','/')
    if set(app.attrib)!={'Id','Executable','EntryPoint'} or app.get('Id')!='App' or app.get('EntryPoint')!='Windows.FullTrustApplication' or not re.fullmatch(r'app/[A-Za-z0-9_.-]+\.exe',executable) or executable not in names or len(app)!=1:raise ValueError('exact native application entry required')
    visual=app[0]
    attrs={'DisplayName','Description','Square150x150Logo','Square44x44Logo','BackgroundColor'}
    if visual.tag!='{'+UAP+'}VisualElements' or set(visual.attrib)!=attrs or len(visual) or visual.get('BackgroundColor')!='transparent' or any(not visual.get(k) or len(visual.get(k))>256 for k in ['DisplayName','Description']):raise ValueError('unsupported application UI declarations')
    for key,name in [('Square150x150Logo','Square150x150Logo.png'),('Square44x44Logo','Square44x44Logo.png')]:
        if visual.get(key,'').replace('\\','/')!='Assets/'+name:raise ValueError('fixed application logos required')
    caps=root.find(ns+'Capabilities')
    if caps.attrib or len(caps)!=2 or {(x.tag,x.get('Name')) for x in caps}!={(ns+'Capability','internetClient'),('{'+RESTRICTED+'}Capability','runFullTrust')} or any(set(x.attrib)!={'Name'} or len(x) for x in caps):raise ValueError('unsupported application capability')
    return {'packageName':data['Name'],'publisher':data['Publisher'],'version':data['Version'],'architecture':'x64','executable':executable}


def content_types(body,names,signed):
    root=xml(body,TYPES,'Types');result={}
    if root.attrib:raise ValueError('invalid content type declarations')
    for child in root:
        kind=child.tag.removeprefix('{'+TYPES+'}')
        attr='Extension' if kind=='Default' else 'PartName' if kind=='Override' else ''
        if not attr or set(child.attrib)!={attr,'ContentType'} or len(child):raise ValueError('unsupported content type declaration')
        value=child.get(attr);content=child.get('ContentType');key=(kind,value)
        if not value or not content or len(content)>200 or key in result:raise ValueError('ambiguous content type declaration')
        if kind=='Override':path_name(value.removeprefix('/'))
        elif not re.fullmatch(r'[A-Za-z0-9]+',value):raise ValueError('invalid content extension')
        result[key]=content
    for name in names:
        if name=='[Content_Types].xml':continue
        content=result.get(('Override','/'+name),result.get(('Default',Path(name).suffix[1:])))
        expected={'AppxManifest.xml':'application/vnd.ms-appx.manifest+xml','AppxBlockMap.xml':'application/vnd.ms-appx.blockmap+xml',SIGNATURE:'application/vnd.ms-appx.signature'}.get(name)
        if not content or expected and content!=expected:raise ValueError('package content type is missing or incorrect')
    # SignTool may add only the signature content type; all others remain pinned.
    return sorted((kind,key,value) for (kind,key),value in result.items() if not ((kind,key) in {('Default','p7x'),('Override','/'+SIGNATURE)} and value=='application/vnd.ms-appx.signature'))


def inspect(path,signed=False,max_expanded=MAX_EXPANDED):
    path=Path(path)
    if path.is_symlink() or not path.is_file() or not 0<path.stat().st_size<=MAX_ARCHIVE:raise ValueError('bounded regular MSIX required')
    try:
        with zipfile.ZipFile(path) as archive:
            entries=archive.infolist();names={};total=0
            if not 1<=len(entries)<=20000:raise ValueError('package entry limit exceeded')
            for info in entries:
                name=path_name(info.filename);mode=info.external_attr>>16
                if name.casefold() in names or info.is_dir() or stat.S_IFMT(mode) not in {0,stat.S_IFREG} or info.flag_bits&1 or info.compress_type not in {zipfile.ZIP_STORED,zipfile.ZIP_DEFLATED}:raise ValueError('ambiguous or special package entry')
                total+=info.file_size
                if info.file_size>MAX_ARCHIVE or total>max_expanded or info.file_size>2000*max(1,info.compress_size):raise ValueError('expanded package limit exceeded')
                names[name.casefold()]=name
            name_set=set(names.values())
            if not {'AppxManifest.xml','AppxBlockMap.xml','[Content_Types].xml'}<=name_set or (SIGNATURE in name_set)!=signed:raise ValueError('MSIX footprint or signature state differs')
            for name in name_set:
                if any('/'.join(name.split('/')[:n]).casefold() in names for n in range(1,len(name.split('/')))):raise ValueError('file/directory path collision')
            metadata=manifest(archive.read('AppxManifest.xml'),name_set)
            normalized_types=content_types(archive.read('[Content_Types].xml'),name_set,signed)
            root=xml(archive.read('AppxBlockMap.xml'),BLOCKS,'BlockMap')
            if root.attrib!={'HashMethod':SHA256}:raise ValueError('SHA256 block map required')
            mapped={};members={};sizes={}
            for item in root:
                if item.tag!='{'+BLOCKS+'}File' or set(item.attrib)!={'Name','Size','LfhSize'}:raise ValueError('unsupported block map file')
                name=path_name(item.get('Name','').replace('\\','/'))
                if name in mapped or name not in name_set or name in FOOTPRINT:raise ValueError('duplicate or foreign mapped file')
                info=archive.getinfo(name)
                if item.get('Size')!=str(info.file_size):raise ValueError('mapped file size differs')
                with path.open('rb') as raw:
                    raw.seek(info.header_offset);header=raw.read(30)
                if len(header)!=30 or header[:4]!=b'PK\x03\x04' or item.get('LfhSize')!=str(30+sum(struct.unpack_from('<HH',header,26))):raise ValueError('mapped local header size differs')
                count=(info.file_size+65535)//65536
                if len(item)!=count:raise ValueError('block count differs')
                h=hashlib.sha256()
                with archive.open(name) as stream:
                    for block in item:
                        chunk=stream.read(65536);h.update(chunk)
                        if block.tag!='{'+BLOCKS+'}Block' or set(block.attrib)-{'Hash','Size'} or len(block) or block.get('Hash')!=base64.b64encode(hashlib.sha256(chunk).digest()).decode():raise ValueError('block hash differs')
                        if 'Size' in block.attrib and not re.fullmatch('[1-9][0-9]{0,9}',block.get('Size')):raise ValueError('invalid stored block size')
                    if stream.read(1):raise ValueError('unmapped data in file')
                mapped[name]=h.hexdigest()
            if set(mapped)!=name_set-FOOTPRINT:raise ValueError('block map does not cover exact payload')
            for name in name_set:
                body=archive.read(name)
                if name==SIGNATURE:
                    if not 8<=len(body)<=8<<20 or not body.startswith(b'PKCX'):raise ValueError('bounded MSIX signature required')
                else:members[name]=hashlib.sha256(body).hexdigest();sizes[name]=len(body)
                if name.lower().endswith(('.exe','.dll')):pe(body,dll=name.lower().endswith('.dll'))
            pe(archive.read(metadata['executable']))
            for name,size in LOGOS.items():png(archive.read('Assets/'+name),size)
            metadata.update(members=members,sizes=sizes,contentTypes=normalized_types,payloadDigest=digest({'mapped':mapped,'contentTypes':normalized_types,'blockMap':members['AppxBlockMap.xml']}))
            return metadata
    except (OSError,zipfile.BadZipFile,KeyError,struct.error,RuntimeError) as error:raise ValueError('invalid or corrupted MSIX') from error


def same_payload(before,after):
    for key in ['packageName','publisher','version','architecture','executable','payloadDigest','contentTypes']:
        if before[key]!=after[key]:raise ValueError('signed MSIX changed approved payload or identity')
    for key in ['members','sizes']:
        if {k:v for k,v in before[key].items() if k!='[Content_Types].xml'}!={k:v for k,v in after[key].items() if k!='[Content_Types].xml'}:raise ValueError('signature changed package members')


def create_layout(release,layout,*,package,publisher: str,version: str,executable,logo,display_name):
    if not PACKAGE.fullmatch(package) or not valid_publisher(publisher) or not valid_version(version) or not re.fullmatch(r'[A-Za-z0-9_.-]+\.exe',executable) or not isinstance(display_name,str) or not 1<=len(display_name)<=100 or any(ord(c)<32 for c in display_name):raise ValueError('invalid Windows package configuration')
    release,layout,logo=Path(release),Path(layout),Path(logo)
    if release.is_symlink() or not release.is_dir() or logo.is_symlink() or not logo.is_dir():raise ValueError('regular release and exact-size logo directory required')
    paths=sorted(release.rglob('*'));seen=set()
    for path in paths:
        relative=path_name(path.relative_to(release).as_posix())
        if path.is_symlink() or not (path.is_dir() or path.is_file()) or relative.casefold() in seen:raise ValueError('unsafe Windows release tree')
        seen.add(relative.casefold())
        if path.is_file() and path.suffix.lower() in {'.exe','.dll'}:pe(path.read_bytes(),path.suffix.lower()=='.dll')
    pe((release/executable).read_bytes())
    for name,size in LOGOS.items():
        path=logo/name
        if path.is_symlink() or not path.is_file():raise ValueError('missing local logo')
        png(path.read_bytes(),size)
    layout.mkdir(mode=0o700);shutil.copytree(release,layout/'app');(layout/'Assets').mkdir()
    for name in LOGOS:shutil.copyfile(logo/name,layout/'Assets'/name)
    manifest_body=f'''<?xml version="1.0" encoding="utf-8"?>
<Package xmlns="{FOUNDATION}" xmlns:uap="{UAP}" xmlns:rescap="{RESTRICTED}" IgnorableNamespaces="uap rescap">
<Identity Name={quoteattr(package)} Publisher={quoteattr(publisher)} Version={quoteattr(version)} ProcessorArchitecture="x64"/>
<Properties><DisplayName>{escape(display_name)}</DisplayName><PublisherDisplayName>{escape(display_name)}</PublisherDisplayName><Logo>Assets/StoreLogo.png</Logo></Properties>
<Dependencies><TargetDeviceFamily Name="Windows.Desktop" MinVersion="10.0.17763.0" MaxVersionTested="10.0.26100.0"/></Dependencies><Resources><Resource Language="en-US"/></Resources>
<Applications><Application Id="App" Executable="app/{executable}" EntryPoint="Windows.FullTrustApplication"><uap:VisualElements DisplayName={quoteattr(display_name)} Description={quoteattr(display_name)} Square150x150Logo="Assets/Square150x150Logo.png" Square44x44Logo="Assets/Square44x44Logo.png" BackgroundColor="transparent"/></Application></Applications>
<Capabilities><Capability Name="internetClient"/><rescap:Capability Name="runFullTrust"/></Capabilities></Package>'''
    (layout/'AppxManifest.xml').write_bytes(manifest_body.encode('utf-8'))
