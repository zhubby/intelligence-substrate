import 'dart:convert';
import 'dart:io';
import 'dart:typed_data';

import 'model.dart';

/// The executable/profile must be provisioned outside application-writable
/// locations by the administrator. This bridge neither elevates nor reconciles.
class ProcessDesktopUpdater implements DesktopUpdater {
  ProcessDesktopUpdater({required this.executable, required this.profile}) {
    if (!File(executable).isAbsolute ||
        !File(profile).isAbsolute ||
        executable.contains('\x00') ||
        profile.contains('\x00') ||
        (Platform.isWindows && !executable.toLowerCase().endsWith('.exe'))) {
      throw ArgumentError(
        'Fixed absolute updater executable and profile required',
      );
    }
  }

  final String executable, profile;
  bool _busy = false;
  @override
  bool get busy => _busy;

  @override
  Future<UpdateSnapshot> run(
    UpdateAction action, {
    String? approvedSha256,
  }) async {
    if (_busy) throw const UpdateFailure('另一项更新操作仍在进行，请稍后刷新状态。');
    if ((action == UpdateAction.install &&
            (approvedSha256 == null ||
                !UpdateSnapshot.digest.hasMatch(approvedSha256))) ||
        (action != UpdateAction.install && approvedSha256 != null)) {
      throw const UpdateFailure('需要确认准确的安装包后才能安装。');
    }
    _busy = true;
    try {
      final process = await Process.start(
        executable,
        [
          '-config',
          profile,
          '-action',
          action.name,
          if (approvedSha256 != null) ...['-approve-sha256', approvedSha256],
        ],
        runInShell: false,
        includeParentEnvironment: false,
        environment: {
          if (Platform.isWindows) ...{
            'SystemRoot': Platform.environment['SystemRoot'] ?? '',
            'TEMP': Directory.systemTemp.path,
            'TMP': Directory.systemTemp.path,
          } else
            'TMPDIR': Directory.systemTemp.path,
        },
      );
      // Do not kill an installer when the dialog closes or output exceeds a
      // bound. Drain both pipes and retain the controller lock until exit.
      final stdout = _collect(process.stdout, 196608);
      final stderr = _collect(process.stderr, 16384);
      final exitCode = await process.exitCode;
      final output = await stdout, errors = await stderr;
      if (output.invalid || errors.invalid) {
        throw const UpdateFailure('更新程序返回了无法确认的结果，请先刷新状态。');
      }
      late UpdateSnapshot snapshot;
      try {
        snapshot = UpdateSnapshot.decode(utf8.decode(output.bytes));
      } on FormatException {
        throw const UpdateFailure('更新程序返回了无法确认的结果，请先刷新状态。');
      }
      if (exitCode != 0) {
        throw UpdateFailure(
          '更新操作未完成，请先刷新状态；若仍无法继续，请联系管理员。',
          snapshot: snapshot,
        );
      }
      final valid = switch (action) {
        UpdateAction.status => true,
        UpdateAction.check => const [
          UpdateState.available,
          UpdateState.staged,
          UpdateState.current,
        ].contains(snapshot.state),
        UpdateAction.stage => snapshot.state == UpdateState.staged,
        UpdateAction.install =>
          snapshot.state == UpdateState.installed &&
              snapshot.sha256 == approvedSha256,
      };
      if (!valid) {
        // Discard a conflicting "success"; it cannot authorize another action.
        throw const UpdateFailure('更新程序返回了无法确认的结果，请先刷新状态。');
      }
      return snapshot;
    } on ProcessException {
      throw const UpdateFailure('无法启动企业更新程序，请联系管理员检查安装配置。');
    } finally {
      _busy = false;
    }
  }
}

class _Output {
  const _Output(this.bytes, this.invalid);
  final Uint8List bytes;
  final bool invalid;
}

Future<_Output> _collect(Stream<List<int>> stream, int limit) async {
  final buffer = BytesBuilder(copy: false);
  var invalid = false;
  try {
    await for (final chunk in stream) {
      final remaining = limit - buffer.length;
      if (chunk.length > remaining) invalid = true;
      if (remaining > 0) {
        buffer.add(
          chunk.length > remaining ? chunk.sublist(0, remaining) : chunk,
        );
      }
    }
  } catch (_) {
    invalid = true;
  }
  return _Output(buffer.takeBytes(), invalid);
}

DesktopUpdater? createDesktopUpdater(String executable, String profile) {
  if ((!Platform.isMacOS && !Platform.isWindows) ||
      executable.isEmpty ||
      profile.isEmpty) {
    return null;
  }
  try {
    return ProcessDesktopUpdater(executable: executable, profile: profile);
  } on ArgumentError {
    return null;
  }
}
