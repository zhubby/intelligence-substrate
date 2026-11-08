"""Bounded iPhoneOS payload inspection; never loads or executes application code."""
from pathlib import Path, PurePosixPath
import struct

from macos_signer import inspect_archive


def native_binary(path, executable=False):
    with Path(path).open('rb') as stream:
        header = stream.read(32)
        if len(header) != 32: raise ValueError('truncated iOS Mach-O header')
        magic, cpu, subtype, kind, count, size, flags, reserved = struct.unpack('<8I', header)
        if magic != 0xfeedfacf or cpu != 0x100000c or subtype != 0 or kind != (2 if executable else 6) or not 1 <= count <= 4096 or not 8 <= size <= 1 << 20:
            raise ValueError('thin arm64 iOS executable/dylib required')
        commands = stream.read(size)
        if len(commands) != size: raise ValueError('truncated iOS load commands')
    offset, platforms = 0, []
    for _ in range(count):
        if offset + 8 > size: raise ValueError('missing iOS load command')
        command, length = struct.unpack_from('<2I', commands, offset)
        if length < 8 or length % 8 or offset + length > size: raise ValueError('invalid iOS load command size')
        if command == 0x32:
            if length < 24: raise ValueError('truncated iOS build-version command')
            platform, minimum, sdk, tools = struct.unpack_from('<4I', commands, offset + 8)
            if length != 24 + tools * 8: raise ValueError('invalid iOS build tools')
            platforms.append(platform)
        elif command in {0x24, 0x25, 0x2f, 0x30}:
            if length != 16: raise ValueError('invalid legacy platform command')
            platforms.append({0x24:1, 0x25:2, 0x2f:3, 0x30:4}[command])
        offset += length
    if offset != size or platforms != [2]: raise ValueError('native binary is not unambiguously iPhoneOS')


def inspect(path, destination):
    metadata = inspect_archive(path, destination, platform='ios')
    app = Path(metadata['app']); root = app.name
    for node in metadata['nodes']:
        parts = PurePosixPath(node['path']).parts
        if node['type'] == 'link' or any(part.endswith(('.appex','.app','.xpc')) for part in parts[1:]):
            raise ValueError('App Store profile supports one flat app and unversioned frameworks')
    import plistlib
    executable = plistlib.loads((app/'Info.plist').read_bytes())['CFBundleExecutable']
    main = root + '/' + executable
    for name in metadata['code']:
        native_binary(app.parent/name, executable=name == main)
    bundles = {root}
    raw = set()
    for name in metadata['code']:
        frameworks = [str(parent) for parent in PurePosixPath(name).parents if str(parent).endswith('.framework')]
        if frameworks: bundles.update(frameworks)
        elif name != main: raw.add(name)
    metadata['signables'] = sorted(bundles | raw, key=lambda name:(-len(PurePosixPath(name).parts),name))
    return metadata
