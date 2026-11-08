import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter/foundation.dart';
import 'package:http/http.dart' as http;

import 'updates/desktop_updater.dart';
import 'updates/model.dart';
import 'updates/update_dialog.dart';

const apiBase = String.fromEnvironment(
  'API_BASE_URL',
  defaultValue: 'http://127.0.0.1:8081',
);
const mode = String.fromEnvironment('APP_MODE', defaultValue: 'feedback');

void main() => runApp(const DeliveryApp());

class DeliveryApp extends StatelessWidget {
  const DeliveryApp({
    super.key,
    this.client,
    this.apiBaseUrl = apiBase,
    this.appMode = mode,
    this.updater,
  });
  final http.Client? client;
  final String apiBaseUrl, appMode;
  final DesktopUpdater? updater;

  @override
  Widget build(BuildContext context) => MaterialApp(
    title: appMode == 'booking' ? '服务预约' : '客户反馈',
    theme: ThemeData(
      fontFamily: 'NotoSansSC',
      fontFamilyFallback: const ['Roboto'],
      colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xff176d5e)),
      useMaterial3: true,
    ),
    home: Home(
      client: client,
      apiBaseUrl: apiBaseUrl,
      appMode: appMode,
      updater: updater,
    ),
  );
}

class Home extends StatefulWidget {
  const Home({
    super.key,
    this.client,
    required this.apiBaseUrl,
    required this.appMode,
    this.updater,
  });
  final http.Client? client;
  final String apiBaseUrl, appMode;
  final DesktopUpdater? updater;

  @override
  State<Home> createState() => _HomeState();
}

class _RequestFailure implements Exception {
  const _RequestFailure(this.message);
  final String message;
  @override
  String toString() => message;
}

class _HomeState extends State<Home> {
  final title = TextEditingController();
  final content = TextEditingController();
  final cancellation = TextEditingController();
  late final http.Client client;
  late final DesktopUpdater? updater;
  bool updateDialogOpen = false;
  List<Map<String, dynamic>> items = [];
  bool pending = false;
  String? error;
  bool get booking => widget.appMode == 'booking';

  @override
  void initState() {
    super.initState();
    client = widget.client ?? http.Client();
    updater = widget.updater ?? configuredDesktopUpdater();
    load();
  }

  @override
  void dispose() {
    if (widget.client == null) client.close();
    title.dispose();
    content.dispose();
    cancellation.dispose();
    super.dispose();
  }

  Future<dynamic> request(String path, [Map<String, String>? body]) async {
    final base = widget.apiBaseUrl.replaceFirst(RegExp(r'/+$'), '');
    final url = kIsWeb && base == '@preview'
        ? Uri.base.resolve('../api/$path')
        : Uri.parse('$base/api/$path');
    late http.Response response;
    try {
      response =
          await (body == null
                  ? client.get(url)
                  : client.post(
                      url,
                      headers: {'Content-Type': 'application/json'},
                      body: jsonEncode(body),
                    ))
              .timeout(const Duration(seconds: 20));
    } on TimeoutException {
      throw _RequestFailure(
        body == null ? '请求超时，请刷新重试。' : '提交结果尚未确认，请先刷新核对，避免重复提交。',
      );
    } on http.ClientException {
      throw _RequestFailure(
        body == null ? '连接失败，请检查网络后刷新。' : '提交结果尚未确认，请先刷新核对，避免重复提交。',
      );
    }
    dynamic result;
    try {
      result = jsonDecode(utf8.decode(response.bodyBytes));
    } on FormatException {
      throw const _RequestFailure('服务返回了无法识别的响应，请刷新重试。');
    }
    if (response.statusCode >= 300) {
      final message = result is Map && result['error'] is String
          ? result['error'] as String
          : '请求未完成，请刷新核对。';
      throw _RequestFailure(message);
    }
    return result;
  }

  Future<void> reloadItems() async {
    final result = await request(booking ? 'slots' : 'feedback');
    if (result is! List ||
        result.any(
          (item) =>
              item is! Map<String, dynamic> ||
              item['id'] is! String ||
              (booking
                  ? item['startsAt'] is! String || item['reserved'] is! bool
                  : item['title'] is! String || item['content'] is! String),
        )) {
      throw const _RequestFailure('服务返回了无法识别的响应，请刷新重试。');
    }
    if (mounted) setState(() => items = result.cast<Map<String, dynamic>>());
  }

  Future<void> load() async {
    if (!mounted || pending) return;
    setState(() {
      pending = true;
      error = null;
    });
    try {
      await reloadItems();
    } catch (failure) {
      if (mounted) setState(() => error = failure.toString());
    } finally {
      if (mounted) setState(() => pending = false);
    }
  }

  Future<void> act(Future<void> Function() action) async {
    // Guard synchronously as two pointer events can arrive before the next frame.
    if (!mounted || pending) return;
    setState(() {
      pending = true;
      error = null;
    });
    try {
      await action();
      if (mounted) await reloadItems();
    } catch (failure) {
      if (mounted) setState(() => error = failure.toString());
    } finally {
      if (mounted) setState(() => pending = false);
    }
  }

  Future<void> submitFeedback() async {
    if (pending) return;
    final heading = title.text.trim(), description = content.text.trim();
    final validation = heading.isEmpty
        ? '请输入反馈标题。'
        : utf8.encode(heading).length > 200
        ? '标题过长，请缩短后提交。'
        : utf8.encode(description).length > 8000
        ? '说明过长，请精简后提交。'
        : null;
    if (validation != null) {
      setState(() => error = validation);
      return;
    }
    await act(() async {
      await request('feedback', {'title': heading, 'content': description});
      if (!mounted) return;
      title.clear();
      content.clear();
    });
  }

  Future<void> reserve(String slotID) => act(() async {
    final result = await request('bookings', {'slotId': slotID});
    if (!mounted) return;
    if (result is! Map ||
        result['bookingToken'] is! String ||
        !RegExp(r'^[a-f0-9]{48}$').hasMatch(result['bookingToken'] as String)) {
      throw const _RequestFailure('预约结果尚未确认，请刷新核对。');
    }
    cancellation.text = result['bookingToken'] as String;
    await showDialog<void>(
      context: context,
      builder: (context) => AlertDialog(
        title: const Text('预约成功'),
        content: SelectableText('请保存取消凭据：\n${result['bookingToken']}'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context),
            child: const Text('已保存'),
          ),
        ],
      ),
    );
  });

  Future<void> cancel() async {
    if (pending) return;
    final token = cancellation.text.trim();
    if (token.isEmpty) {
      setState(() => error = '请输入取消凭据。');
      return;
    }
    await act(() async {
      await request('bookings/cancel', {'bookingToken': token});
      if (mounted) cancellation.clear();
    });
  }

  Future<void> showUpdates() async {
    if (updater == null || updateDialogOpen || !mounted) return;
    setState(() => updateDialogOpen = true);
    try {
      await showDialog<void>(
        context: context,
        barrierDismissible: false,
        builder: (_) => UpdateDialog(updater: updater!),
      );
    } finally {
      if (mounted) setState(() => updateDialogOpen = false);
    }
  }

  @override
  Widget build(BuildContext context) => Scaffold(
    appBar: AppBar(
      title: Text(booking ? '服务预约' : '客户反馈'),
      actions: [
        if (updater != null)
          IconButton(
            onPressed: updateDialogOpen ? null : showUpdates,
            icon: const Icon(Icons.system_update_alt),
            tooltip: '应用更新',
          ),
        IconButton(
          onPressed: pending ? null : load,
          icon: const Icon(Icons.refresh),
          tooltip: '刷新',
        ),
      ],
    ),
    body: Align(
      alignment: Alignment.topCenter,
      child: SizedBox(
        width: 720,
        child: ListView(
          padding: const EdgeInsets.all(24),
          children: [
            if (pending) const LinearProgressIndicator(),
            if (error != null)
              Padding(
                padding: const EdgeInsets.symmetric(vertical: 16),
                child: Semantics(
                  liveRegion: true,
                  child: Text(
                    error!,
                    style: TextStyle(
                      color: Theme.of(context).colorScheme.error,
                    ),
                  ),
                ),
              ),
            if (!booking) ...[
              Text('分享你的想法', style: Theme.of(context).textTheme.headlineSmall),
              const SizedBox(height: 20),
              TextField(
                controller: title,
                enabled: !pending,
                decoration: const InputDecoration(labelText: '反馈标题'),
              ),
              TextField(
                controller: content,
                enabled: !pending,
                decoration: const InputDecoration(labelText: '详细说明'),
                minLines: 3,
                maxLines: 6,
              ),
              const SizedBox(height: 16),
              FilledButton(
                onPressed: pending ? null : submitFeedback,
                child: const Text('提交反馈'),
              ),
              const SizedBox(height: 32),
              Text('最新反馈', style: Theme.of(context).textTheme.titleLarge),
              if (items.isEmpty && !pending)
                const Padding(
                  padding: EdgeInsets.all(24),
                  child: Text('还没有反馈，欢迎提交第一条。'),
                ),
              ...items.map(
                (item) => Card(
                  child: ListTile(
                    title: Text(item['title'] as String),
                    subtitle: Text(item['content'] as String),
                  ),
                ),
              ),
            ] else ...[
              Text('选择服务时间', style: Theme.of(context).textTheme.headlineSmall),
              const SizedBox(height: 20),
              if (items.isEmpty && !pending) const Text('暂时没有可预约的时段。'),
              ...items.map(
                (item) => Card(
                  child: ListTile(
                    title: Text(item['startsAt'] as String),
                    subtitle: Text(item['reserved'] == true ? '已预约' : '可以预约'),
                    trailing: FilledButton(
                      onPressed: pending || item['reserved'] == true
                          ? null
                          : () => reserve(item['id'] as String),
                      child: const Text('预约'),
                    ),
                  ),
                ),
              ),
              const SizedBox(height: 32),
              TextField(
                controller: cancellation,
                enabled: !pending,
                decoration: const InputDecoration(labelText: '取消凭据'),
              ),
              OutlinedButton(
                onPressed: pending ? null : cancel,
                child: const Text('取消我的预约'),
              ),
            ],
          ],
        ),
      ),
    ),
  );
}
