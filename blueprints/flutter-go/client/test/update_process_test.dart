import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';

import 'package:delivery_app/updates/model.dart';
import 'package:delivery_app/updates/process_updater.dart';

Map<String, Object?> candidate(String state) => {
  'state': state,
  'minimumSequence': 4,
  'feed': {
    'schemaVersion': 1,
    'app': 'feedback',
    'target': 'macos',
    'channel': 'stable',
    'version': '1.2.0',
    'sha256': 'a' * 64,
    'size': 123456,
    'filename': 'feedback-1.2.0.pkg',
    'sequence': 4,
  },
  if (state == 'staged') 'packagePath': '/private/stage/feedback-1.2.0.pkg',
  if (state == 'installing' || state == 'reconciliation_required')
    'attemptId': 'b' * 48,
  if (state == 'installed') 'installedVersion': '1.2.0',
};

void main() {
  late Directory temporary;
  late File profile, log;
  late String executable;

  setUpAll(() async {
    temporary = await Directory.systemTemp.createTemp('update-bridge-');
    // Compile a dependency-free child process with this Flutter SDK. The
    // fixture replaces only the external CLI; transport/decoding stay real.
    final configFile = File('.dart_tool/package_config.json').absolute;
    final packages =
        jsonDecode(await configFile.readAsString())['packages'] as List;
    final flutter = packages.firstWhere((p) => p['name'] == 'flutter');
    final sdk = Directory.fromUri(
      configFile.uri.resolve(flutter['rootUri']),
    ).uri.resolve('../../');
    final dart = sdk
        .resolve(
          'bin/cache/dart-sdk/bin/${Platform.isWindows ? 'dart.exe' : 'dart'}',
        )
        .toFilePath();
    executable =
        '${temporary.path}/${Platform.isWindows ? 'helper.exe' : 'helper'}';
    final compiled = await Process.run(dart, [
      'compile',
      'exe',
      'test/fixtures/updater_process.dart',
      '-o',
      executable,
    ]);
    expect(
      compiled.exitCode,
      0,
      reason: '${compiled.stdout}\n${compiled.stderr}',
    );
  });
  tearDownAll(() async => temporary.delete(recursive: true));

  setUp(() async {
    final directory = await temporary.createTemp('case with spaces ');
    profile = File('${directory.path}/profile \$literal.json');
    log = File('${directory.path}/calls.jsonl');
  });

  Future<ProcessDesktopUpdater> helper({
    Map<String, Object?>? snapshot,
    int exitCode = 0,
    String? gate,
    int outputBytes = 0,
  }) async {
    await profile.writeAsString(
      jsonEncode({
        'snapshot': snapshot ?? candidate('staged'),
        'exitCode': exitCode,
        'log': log.path,
        'gate': gate,
        'outputBytes': outputBytes,
      }),
    );
    return ProcessDesktopUpdater(executable: executable, profile: profile.path);
  }

  test(
    'install passes only the captured exact digest as a separate argument',
    () async {
      final updater = await helper(snapshot: candidate('installed'));
      final snapshot = await updater.run(
        UpdateAction.install,
        approvedSha256: 'a' * 64,
      );
      expect(snapshot.state, UpdateState.installed);
      final call = jsonDecode(await log.readAsString());
      expect(call['args'], [
        '-config',
        profile.path,
        '-action',
        'install',
        '-approve-sha256',
        'a' * 64,
      ]);
      expect(call['hasPath'], isFalse);
      expect(call['hasHome'], isFalse);
    },
  );

  test('invalid or misplaced approval never starts a process', () async {
    final updater = await helper();
    for (final action in UpdateAction.values) {
      await expectLater(
        updater.run(
          action,
          approvedSha256: action == UpdateAction.install ? 'bad' : 'a' * 64,
        ),
        throwsA(isA<UpdateFailure>()),
      );
    }
    expect(await log.exists(), isFalse);
  });

  test(
    'relative executable and profile paths fail before invocation',
    () async {
      expect(
        () => ProcessDesktopUpdater(
          executable: 'is-updater',
          profile: '/profile.json',
        ),
        throwsArgumentError,
      );
      expect(
        () => ProcessDesktopUpdater(
          executable: executable,
          profile: 'profile.json',
        ),
        throwsArgumentError,
      );
      expect(await log.exists(), isFalse);
    },
  );

  test(
    'unknown states, missing staged identity and malformed JSON fail closed',
    () async {
      for (final invalid in [
        {'state': 'success'},
        {
          ...candidate('staged'),
          'feed': {'version': '1.2.0'},
        },
        {...candidate('staged'), 'packagePath': ''},
        {...candidate('installed'), 'installedVersion': '1.1.0'},
        {...candidate('staged'), 'minimumSequence': 3},
      ]) {
        final updater = await helper(snapshot: invalid);
        await expectLater(
          updater.run(UpdateAction.status),
          throwsA(isA<UpdateFailure>()),
        );
      }
      expect(
        () => UpdateSnapshot.decode('{"state":'),
        throwsA(isA<FormatException>()),
      );
    },
  );

  test(
    'nonzero exit retains authoritative uncertain state without reporting success',
    () async {
      final updater = await helper(
        snapshot: candidate('reconciliation_required'),
        exitCode: 1,
      );
      try {
        await updater.run(UpdateAction.install, approvedSha256: 'a' * 64);
        fail('native failure must not look successful');
      } on UpdateFailure catch (failure) {
        expect(failure.snapshot!.state, UpdateState.reconciliationRequired);
        expect(failure.snapshot!.attemptId, 'b' * 48);
      }
      expect((await log.readAsLines()).length, 1);
    },
  );

  test('wrong candidate cannot acknowledge an approved installation', () async {
    final wrong = candidate('installed');
    wrong['feed'] = {...wrong['feed'] as Map, 'sha256': 'c' * 64};
    final updater = await helper(snapshot: wrong);
    await expectLater(
      updater.run(UpdateAction.install, approvedSha256: 'a' * 64),
      throwsA(isA<UpdateFailure>()),
    );
  });

  test(
    'a waiting operation excludes a second process and releases after exit',
    () async {
      final gate = File('${profile.parent.path}/continue');
      final updater = await helper(gate: gate.path);
      final first = updater.run(UpdateAction.status);
      expect(updater.busy, isTrue);
      await expectLater(
        updater.run(UpdateAction.check),
        throwsA(isA<UpdateFailure>()),
      );
      await gate.writeAsString('continue');
      expect((await first).state, UpdateState.staged);
      expect(updater.busy, isFalse);
      expect((await log.readAsLines()).length, 1);
    },
  );

  test(
    'oversized process output is drained but cannot create a valid snapshot',
    () async {
      final updater = await helper(outputBytes: 300000);
      await expectLater(
        updater.run(UpdateAction.status),
        throwsA(isA<UpdateFailure>()),
      );
      expect(updater.busy, isFalse);
    },
  );

  test(
    'process startup failure cannot leave the controller permanently busy',
    () async {
      final updater = ProcessDesktopUpdater(
        executable: '$executable-missing',
        profile: profile.path,
      );
      await expectLater(
        updater.run(UpdateAction.status),
        throwsA(isA<UpdateFailure>()),
      );
      expect(updater.busy, isFalse);
    },
  );
}
