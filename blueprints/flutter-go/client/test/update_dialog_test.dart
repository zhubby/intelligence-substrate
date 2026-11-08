import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import 'package:delivery_app/main.dart';
import 'package:delivery_app/updates/model.dart';
import 'package:delivery_app/updates/update_dialog.dart';

UpdateSnapshot sample(String state, {String? digest}) => UpdateSnapshot.decode(
  jsonEncode({
    'state': state,
    'minimumSequence': 2,
    'feed': {
      'schemaVersion': 1,
      'version': '1.2.0',
      'sha256': digest ?? 'a' * 64,
      'filename': 'feedback-1.2.0.pkg',
      'size': 123456,
      'sequence': 2,
    },
    if (state == 'staged') 'packagePath': '/private/package.pkg',
    if (state == 'installed') 'installedVersion': '1.2.0',
    if (state == 'installing' || state == 'reconciliation_required')
      'attemptId': 'b' * 48,
  }),
);

class TestUpdater implements DesktopUpdater {
  TestUpdater(this.respond);
  final FutureOr<UpdateSnapshot> Function(UpdateAction action) respond;
  final actions = <UpdateAction>[];
  final approvals = <String?>[];
  @override
  bool get busy => false;
  @override
  Future<UpdateSnapshot> run(
    UpdateAction action, {
    String? approvedSha256,
  }) async {
    actions.add(action);
    approvals.add(approvedSha256);
    return respond(action);
  }
}

Future<void> open(WidgetTester tester, TestUpdater updater) async {
  await tester.pumpWidget(
    MaterialApp(
      home: Builder(
        builder: (context) => Scaffold(
          body: TextButton(
            onPressed: () => showDialog<void>(
              context: context,
              barrierDismissible: false,
              builder: (_) => UpdateDialog(updater: updater),
            ),
            child: const Text('open'),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('open'));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('both application modes expose only a configured update entry', (
    tester,
  ) async {
    for (final mode in ['feedback', 'booking']) {
      final client = MockClient((_) async => http.Response('[]', 200));
      await tester.pumpWidget(DeliveryApp(client: client, appMode: mode));
      await tester.pumpAndSettle();
      expect(find.byTooltip('应用更新'), findsNothing);
      final updater = TestUpdater((_) => sample('available'));
      await tester.pumpWidget(
        DeliveryApp(
          key: ValueKey(mode),
          client: client,
          appMode: mode,
          updater: updater,
        ),
      );
      await tester.pumpAndSettle();
      await tester.tap(find.byTooltip('应用更新'));
      await tester.pumpAndSettle();
      expect(find.textContaining('可下载版本 1.2.0'), findsOneWidget);
      expect(updater.actions, [UpdateAction.status]);
      await tester.tap(find.text('关闭'));
      await tester.pumpAndSettle();
    }
  });

  testWidgets('canceling exact-package consent never invokes install', (
    tester,
  ) async {
    final updater = TestUpdater((_) => sample('staged'));
    await open(tester, updater);
    await tester.tap(find.text('安装更新'));
    await tester.pumpAndSettle();
    expect(find.textContaining('请先保存工作'), findsOneWidget);
    expect(find.text('a' * 64), findsWidgets);
    await tester.tap(find.text('暂不安装'));
    await tester.pumpAndSettle();
    expect(updater.actions, [UpdateAction.status]);
    expect(find.text('安装更新'), findsOneWidget);
  });

  testWidgets(
    'confirmation captures displayed bytes and duplicate clicks dispatch once',
    (tester) async {
      final installed = Completer<UpdateSnapshot>();
      final updater = TestUpdater(
        (action) => action == UpdateAction.install
            ? installed.future
            : sample('staged'),
      );
      await open(tester, updater);
      await tester.tap(find.text('安装更新'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('确认安装'));
      await tester.tap(find.text('确认安装'), warnIfMissed: false);
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 300));
      expect(updater.actions, [UpdateAction.status, UpdateAction.install]);
      expect(updater.approvals.last, 'a' * 64);
      expect(
        tester
            .widget<TextButton>(find.widgetWithText(TextButton, '关闭'))
            .onPressed,
        isNull,
      );
      await tester.binding.handlePopRoute();
      await tester.pump();
      expect(find.byType(UpdateDialog), findsOneWidget);
      installed.complete(sample('installed'));
      await tester.pumpAndSettle();
      expect(find.textContaining('系统安装记录已确认版本 1.2.0'), findsOneWidget);
      expect(find.text('安装更新'), findsNothing);
    },
  );

  testWidgets(
    'download stages bytes but installation still requires a new consent',
    (tester) async {
      final updater = TestUpdater(
        (action) =>
            sample(action == UpdateAction.stage ? 'staged' : 'available'),
      );
      await open(tester, updater);
      await tester.tap(find.text('下载并校验'));
      await tester.pumpAndSettle();
      expect(updater.actions, [UpdateAction.status, UpdateAction.stage]);
      expect(find.text('安装更新'), findsOneWidget);
      expect(find.text('确认安装'), findsNothing);
    },
  );

  testWidgets(
    'restarted uncertain installation offers status but no retry or reconcile',
    (tester) async {
      for (final state in ['installing', 'reconciliation_required']) {
        final updater = TestUpdater((_) => sample(state));
        await open(tester, updater);
        expect(find.textContaining('联系管理员核对'), findsOneWidget);
        expect(find.textContaining('b' * 48), findsOneWidget);
        expect(find.text('检查更新'), findsNothing);
        expect(find.text('安装更新'), findsNothing);
        expect(find.text('下载并校验'), findsNothing);
        await tester.tap(find.text('刷新状态'));
        await tester.pumpAndSettle();
        expect(updater.actions, [UpdateAction.status, UpdateAction.status]);
        await tester.tap(find.text('关闭'));
        await tester.pumpAndSettle();
      }
    },
  );

  testWidgets(
    'unknown install outcome requires authoritative status before any further action',
    (tester) async {
      var failed = false;
      final updater = TestUpdater((action) {
        if (action == UpdateAction.install) {
          failed = true;
          throw const UpdateFailure('无法确认结果，请刷新状态。');
        }
        return sample(failed ? 'installing' : 'staged');
      });
      await open(tester, updater);
      await tester.tap(find.text('安装更新'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('确认安装'));
      await tester.pumpAndSettle();
      expect(find.text('无法确认结果，请刷新状态。'), findsOneWidget);
      expect(find.text('安装更新'), findsNothing);
      expect(find.text('检查更新'), findsNothing);
      await tester.tap(find.text('刷新状态'));
      await tester.pumpAndSettle();
      expect(find.textContaining('联系管理员核对'), findsOneWidget);
      expect(updater.actions, [
        UpdateAction.status,
        UpdateAction.install,
        UpdateAction.status,
      ]);
    },
  );

  testWidgets(
    'small viewport and long identity stay scrollable without overflow',
    (tester) async {
      tester.view.physicalSize = const Size(390, 844);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      await open(tester, TestUpdater((_) => sample('staged')));
      await tester.tap(find.text('安装更新'));
      await tester.pumpAndSettle();
      expect(find.text('确认安装'), findsOneWidget);
      expect(tester.takeException(), isNull);
      await tester.tap(find.text('暂不安装'));
      await tester.pumpAndSettle();
      await tester.tap(find.text('关闭'));
      await tester.pumpAndSettle();
      expect(find.byType(UpdateDialog), findsNothing);
    },
  );

  testWidgets(
    'disposing the application while status is pending has no late UI writes',
    (tester) async {
      final status = Completer<UpdateSnapshot>();
      await tester.pumpWidget(
        MaterialApp(
          home: UpdateDialog(updater: TestUpdater((_) => status.future)),
        ),
      );
      await tester.pump();
      await tester.pumpWidget(const SizedBox());
      status.complete(sample('staged'));
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
    },
  );
}
