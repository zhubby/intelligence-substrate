import 'dart:convert';

enum UpdateAction { status, check, stage, install }

enum UpdateState {
  unchecked,
  available,
  staged,
  current,
  installing,
  reconciliationRequired,
  installed,
}

abstract interface class DesktopUpdater {
  bool get busy;
  Future<UpdateSnapshot> run(UpdateAction action, {String? approvedSha256});
}

class UpdateFailure implements Exception {
  const UpdateFailure(this.message, {this.snapshot});
  final String message;
  final UpdateSnapshot? snapshot;
  @override
  String toString() => message;
}

class UpdateSnapshot {
  const UpdateSnapshot._({
    required this.state,
    this.version,
    this.sha256,
    this.filename,
    this.size,
    this.installedVersion,
    this.attemptId,
  });
  final UpdateState state;
  final String? version, sha256, filename, installedVersion, attemptId;
  final int? size;

  bool get requiresReconciliation =>
      state == UpdateState.installing ||
      state == UpdateState.reconciliationRequired;

  static final digest = RegExp(r'^[a-f0-9]{64}$');
  static final _version = RegExp(
    r'^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$',
  );

  factory UpdateSnapshot.decode(String output) {
    final data = jsonDecode(output);
    if (data is! Map<String, dynamic> ||
        data['feed'] is! Map<String, dynamic>) {
      throw const FormatException('Invalid update snapshot');
    }
    final state = switch (data['state']) {
      'unchecked' => UpdateState.unchecked,
      'available' => UpdateState.available,
      'staged' => UpdateState.staged,
      'current' => UpdateState.current,
      'installing' => UpdateState.installing,
      'reconciliation_required' => UpdateState.reconciliationRequired,
      'installed' => UpdateState.installed,
      _ => throw const FormatException('Unknown update state'),
    };
    if (state == UpdateState.unchecked) {
      if (data['minimumSequence'] != 0) {
        throw const FormatException('Invalid unchecked sequence');
      }
      return UpdateSnapshot._(state: state);
    }
    final feed = data['feed'] as Map<String, dynamic>;
    final version = feed['version'], sha256 = feed['sha256'];
    final filename = feed['filename'], size = feed['size'];
    final installed = data['installedVersion'], attempt = data['attemptId'];
    if (feed['schemaVersion'] != 1 ||
        feed['sequence'] is! int ||
        (feed['sequence'] as int) < 1 ||
        data['minimumSequence'] != feed['sequence'] ||
        version is! String ||
        !_version.hasMatch(version) ||
        sha256 is! String ||
        !digest.hasMatch(sha256) ||
        filename is! String ||
        !RegExp(r'^[a-zA-Z0-9][a-zA-Z0-9._-]{0,159}$').hasMatch(filename) ||
        size is! int ||
        size < 1 ||
        size > 512 * 1024 * 1024 ||
        (installed != null &&
            (installed is! String || !_version.hasMatch(installed))) ||
        (state == UpdateState.staged &&
            (data['packagePath'] is! String ||
                (data['packagePath'] as String).isEmpty)) ||
        ((state == UpdateState.installing ||
                state == UpdateState.reconciliationRequired) &&
            (attempt is! String ||
                !RegExp(r'^[a-f0-9]{48}$').hasMatch(attempt))) ||
        (state == UpdateState.installed && installed != version) ||
        (state == UpdateState.current && installed == null)) {
      throw const FormatException('Incomplete update identity or state');
    }
    return UpdateSnapshot._(
      state: state,
      version: version,
      sha256: sha256,
      filename: filename,
      size: size,
      installedVersion: installed as String?,
      attemptId: attempt is String ? attempt : null,
    );
  }
}
