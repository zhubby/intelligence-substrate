// Status-only probe for the real external updater. Never calls a feed, downloads
// a package or installs. Use a dedicated profile/state for local acceptance.
// Relative imports keep this copied tool independent of the generated app name.
// ignore_for_file: avoid_relative_lib_imports
import 'dart:convert';
import 'dart:io';

import '../lib/updates/model.dart';
import '../lib/updates/process_updater.dart';

Future<void> main(List<String> arguments) async {
  if (arguments.length != 2) {
    throw ArgumentError(
      'Usage: dart run tool/probe_updater.dart ABSOLUTE_EXECUTABLE ABSOLUTE_PROFILE',
    );
  }
  final updater = ProcessDesktopUpdater(
    executable: arguments[0],
    profile: arguments[1],
  );
  final first = await updater.run(UpdateAction.status);
  final second = await updater.run(UpdateAction.status);
  if (first.state != second.state ||
      first.sha256 != second.sha256 ||
      first.version != second.version) {
    throw StateError(
      'State changed between status-only processes; investigate concurrent activity',
    );
  }
  stdout.writeln(
    jsonEncode({
      'action': 'status',
      'state': second.state.name,
      'version': second.version,
      'processInvocations': 2,
      'nativeInstallationExecuted': false,
      'remoteFeedRequested': false,
    }),
  );
}
