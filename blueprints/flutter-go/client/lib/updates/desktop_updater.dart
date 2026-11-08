import 'model.dart';
import 'unsupported_updater.dart'
    if (dart.library.io) 'process_updater.dart'
    as platform;

/// Opt-in deployment paths are fixed by the application build, never by a feed,
/// environment variable, deep link or a file selected in the application UI.
DesktopUpdater? configuredDesktopUpdater() => platform.createDesktopUpdater(
  const String.fromEnvironment('UPDATE_EXECUTABLE'),
  const String.fromEnvironment('UPDATE_PROFILE'),
);
