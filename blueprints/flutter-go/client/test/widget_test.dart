import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import 'package:delivery_app/main.dart';

http.Response jsonResponse(Object data, [int status = 200]) => http.Response(
  jsonEncode(data),
  status,
  headers: {'content-type': 'application/json; charset=utf-8'},
);

Future<void> start(
  WidgetTester tester,
  MockClient client, {
  String appMode = 'feedback',
}) async {
  await tester.pumpWidget(
    DeliveryApp(
      client: client,
      apiBaseUrl: 'https://api.example.test',
      appMode: appMode,
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('empty feedback is rejected before any write', (tester) async {
    var writes = 0;
    await start(
      tester,
      MockClient((request) async {
        if (request.method == 'POST') writes++;
        return jsonResponse([]);
      }),
    );
    await tester.tap(find.text('提交反馈'));
    await tester.pumpAndSettle();
    expect(writes, 0);
    expect(find.text('请输入反馈标题。'), findsOneWidget);
  });

  testWidgets('UTF-8 title and content limits match the API', (tester) async {
    var writes = 0;
    await start(
      tester,
      MockClient((request) async {
        if (request.method == 'POST') writes++;
        return jsonResponse([]);
      }),
    );
    await tester.enterText(find.byType(TextField).first, '汉' * 70);
    await tester.tap(find.text('提交反馈'));
    await tester.pumpAndSettle();
    expect(find.text('标题过长，请缩短后提交。'), findsOneWidget);
    await tester.enterText(find.byType(TextField).first, 'valid');
    await tester.enterText(find.byType(TextField).at(1), '汉' * 2700);
    await tester.tap(find.text('提交反馈'));
    await tester.pumpAndSettle();
    expect(find.text('说明过长，请精简后提交。'), findsOneWidget);
    expect(writes, 0);
  });

  testWidgets('a pending write cannot be submitted twice', (tester) async {
    final response = Completer<http.Response>();
    var writes = 0;
    await start(
      tester,
      MockClient((request) async {
        if (request.method == 'POST') {
          writes++;
          return response.future;
        }
        return jsonResponse([]);
      }),
    );
    await tester.enterText(find.byType(TextField).first, 'One feedback');
    await tester.tap(find.text('提交反馈'));
    await tester.tap(find.text('提交反馈'));
    await tester.pump();
    expect(writes, 1);
    expect(
      tester
          .widget<FilledButton>(find.widgetWithText(FilledButton, '提交反馈'))
          .onPressed,
      isNull,
    );
    response.complete(
      jsonResponse({
        'id': 'f1',
        'title': 'One feedback',
        'content': '',
        'createdAt': '2026-11-05T00:00:00Z',
      }, 201),
    );
    await tester.pumpAndSettle();
    expect(
      tester.widget<TextField>(find.byType(TextField).first).controller!.text,
      isEmpty,
    );
  });

  testWidgets('a failed read can be refreshed without losing the form', (
    tester,
  ) async {
    var reads = 0;
    await start(
      tester,
      MockClient((request) async {
        if (reads++ == 0) throw http.ClientException('offline');
        return jsonResponse([
          {
            'id': 'f1',
            'title': 'Recovered item',
            'content': 'plain',
            'createdAt': '2026-11-05T00:00:00Z',
          },
        ]);
      }),
    );
    expect(find.text('连接失败，请检查网络后刷新。'), findsOneWidget);
    await tester.enterText(find.byType(TextField).first, 'Unsaved idea');
    await tester.tap(find.byTooltip('刷新'));
    await tester.pumpAndSettle();
    expect(find.text('Recovered item'), findsOneWidget);
    expect(
      tester.widget<TextField>(find.byType(TextField).first).controller!.text,
      'Unsaved idea',
    );
    expect(find.text('连接失败，请检查网络后刷新。'), findsNothing);
  });

  testWidgets(
    'failed writes preserve input and are never retried automatically',
    (tester) async {
      var writes = 0;
      await start(
        tester,
        MockClient((request) async {
          if (request.method == 'POST') {
            writes++;
            return jsonResponse({'error': 'Feedback capacity reached'}, 503);
          }
          return jsonResponse([]);
        }),
      );
      await tester.enterText(find.byType(TextField).first, 'Preserve me');
      await tester.tap(find.text('提交反馈'));
      await tester.pumpAndSettle();
      expect(find.text('Feedback capacity reached'), findsOneWidget);
      expect(
        tester.widget<TextField>(find.byType(TextField).first).controller!.text,
        'Preserve me',
      );
      expect(writes, 1);
    },
  );

  testWidgets('invalid JSON becomes a recoverable response error', (
    tester,
  ) async {
    await start(
      tester,
      MockClient(
        (request) async => http.Response('<html>bad gateway</html>', 502),
      ),
    );
    expect(find.text('服务返回了无法识别的响应，请刷新重试。'), findsOneWidget);
    expect(
      tester
          .widget<IconButton>(
            find.byWidgetPredicate(
              (widget) => widget is IconButton && widget.tooltip == '刷新',
            ),
          )
          .onPressed,
      isNotNull,
    );
  });

  testWidgets('reservation keeps its opaque cancellation credential visible', (
    tester,
  ) async {
    var reserved = false;
    await start(
      tester,
      MockClient((request) async {
        if (request.method == 'POST') {
          reserved = true;
          return jsonResponse({'bookingToken': 'a' * 48, 'slotId': 's1'}, 201);
        }
        return jsonResponse([
          {
            'id': 's1',
            'startsAt': '2026-12-01T09:00:00+08:00',
            'reserved': reserved,
          },
        ]);
      }),
      appMode: 'booking',
    );
    await tester.tap(find.widgetWithText(FilledButton, '预约'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.text('预约成功'), findsOneWidget);
    expect(
      find.descendant(
        of: find.byType(AlertDialog),
        matching: find.textContaining('a' * 48),
      ),
      findsOneWidget,
    );
    await tester.tap(find.text('已保存'));
    await tester.pumpAndSettle();
    expect(find.text('已预约'), findsOneWidget);
    expect(
      tester.widget<TextField>(find.byType(TextField)).controller!.text,
      'a' * 48,
    );
  });

  testWidgets('booking conflicts and cancellation errors remain visible', (
    tester,
  ) async {
    await start(
      tester,
      MockClient((request) async {
        if (request.url.path.endsWith('/cancel')) {
          return jsonResponse({'error': 'Booking not found'}, 404);
        }
        if (request.method == 'POST') {
          return jsonResponse({'error': 'This slot is already reserved'}, 409);
        }
        return jsonResponse([
          {
            'id': 's1',
            'startsAt': '2026-12-01T09:00:00+08:00',
            'reserved': false,
          },
        ]);
      }),
      appMode: 'booking',
    );
    await tester.tap(find.widgetWithText(FilledButton, '预约'));
    await tester.pumpAndSettle();
    expect(find.text('This slot is already reserved'), findsOneWidget);
    await tester.enterText(find.byType(TextField), 'invalid-token');
    await tester.tap(find.text('取消我的预约'));
    await tester.pumpAndSettle();
    expect(find.text('Booking not found'), findsOneWidget);
    expect(
      tester.widget<TextField>(find.byType(TextField)).controller!.text,
      'invalid-token',
    );
  });

  testWidgets(
    'leaving during a write never updates disposed state or controllers',
    (tester) async {
      final response = Completer<http.Response>();
      await start(
        tester,
        MockClient(
          (request) async =>
              request.method == 'POST' ? response.future : jsonResponse([]),
        ),
      );
      await tester.enterText(
        find.byType(TextField).first,
        'Leave while saving',
      );
      await tester.tap(find.text('提交反馈'));
      await tester.pump();
      await tester.pumpWidget(const SizedBox());
      response.complete(
        jsonResponse({
          'id': 'f1',
          'title': 'Leave while saving',
          'content': '',
          'createdAt': '2026-11-05T00:00:00Z',
        }, 201),
      );
      await tester.pumpAndSettle();
      expect(tester.takeException(), isNull);
    },
  );
}
