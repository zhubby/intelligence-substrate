import 'package:flutter/material.dart';

import 'model.dart';

class UpdateDialog extends StatefulWidget {
  const UpdateDialog({super.key, required this.updater});
  final DesktopUpdater updater;

  @override
  State<UpdateDialog> createState() => _UpdateDialogState();
}

class _UpdateDialogState extends State<UpdateDialog> {
  UpdateSnapshot? snapshot;
  bool pending = false, confirming = false, needsStatus = true;
  String? error;
  String progress = '正在读取更新状态…';

  @override
  void initState() {
    super.initState();
    act(UpdateAction.status);
  }

  Future<void> act(UpdateAction action, {String? approvedSha256}) async {
    if (!mounted || pending || confirming) return;
    setState(() {
      pending = true;
      error = null;
      progress = switch (action) {
        UpdateAction.status => '正在读取更新状态…',
        UpdateAction.check => '正在检查更新…',
        UpdateAction.stage => '正在下载并校验安装包…',
        UpdateAction.install => '正在安装，请等待系统完成；应用可能会关闭…',
      };
    });
    try {
      final next = await widget.updater.run(
        action,
        approvedSha256: approvedSha256,
      );
      if (!mounted) return;
      setState(() {
        snapshot = next;
        needsStatus = false;
      });
    } on UpdateFailure catch (failure) {
      if (!mounted) return;
      setState(() {
        snapshot = failure.snapshot;
        error = failure.message;
        needsStatus = true;
      });
    } catch (_) {
      if (!mounted) return;
      setState(() {
        snapshot = null;
        error = '更新结果暂时无法确认，请先刷新状态。';
        needsStatus = true;
      });
    } finally {
      if (mounted) setState(() => pending = false);
    }
  }

  Future<void> confirmInstall() async {
    final candidate = snapshot;
    if (!mounted ||
        pending ||
        confirming ||
        needsStatus ||
        candidate?.state != UpdateState.staged) {
      return;
    }
    setState(() => confirming = true);
    final approved = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('安装版本 ${candidate!.version}？'),
        scrollable: true,
        content: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const Text('请先保存工作。安装可能关闭应用，完成后请重新打开并检查功能。'),
            const SizedBox(height: 16),
            Text(candidate.filename!),
            const SizedBox(height: 8),
            const Text('此安装包的 SHA256'),
            SelectableText(candidate.sha256!),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('暂不安装'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('确认安装'),
          ),
        ],
      ),
    );
    if (!mounted) return;
    setState(() => confirming = false);
    if (approved == true) {
      // Consent refers to the displayed immutable candidate, even if another
      // process has checked a different release in the intervening time.
      await act(UpdateAction.install, approvedSha256: candidate!.sha256);
    }
  }

  String describe(UpdateSnapshot value) => switch (value.state) {
    UpdateState.unchecked => '尚未检查更新。',
    UpdateState.available => '可下载版本 ${value.version}。',
    UpdateState.staged => '已下载并验证版本 ${value.version}。',
    UpdateState.current => '系统已安装版本 ${value.installedVersion}，无需安装该候选。',
    UpdateState.installing ||
    UpdateState.reconciliationRequired => '安装可能仍在进行或结果尚未确认。请联系管理员核对；此处只能刷新状态。',
    UpdateState.installed =>
      '系统安装记录已确认版本 ${value.installedVersion}。请重新打开应用并检查功能。',
  };

  @override
  Widget build(BuildContext context) {
    final value = snapshot;
    final canAct =
        !needsStatus && value != null && !value.requiresReconciliation;
    return PopScope(
      canPop: !pending && !confirming,
      child: AlertDialog(
        title: const Text('应用更新'),
        scrollable: true,
        content: SizedBox(
          width: 460,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              if (pending) ...[
                const LinearProgressIndicator(),
                const SizedBox(height: 12),
                Semantics(liveRegion: true, child: Text(progress)),
              ],
              if (value != null) ...[
                Text(describe(value)),
                if (value.installedVersion != null &&
                    value.state != UpdateState.installed &&
                    value.state != UpdateState.current)
                  Text('系统安装版本：${value.installedVersion}'),
                if (value.filename != null &&
                    !value.requiresReconciliation) ...[
                  const SizedBox(height: 12),
                  Text(
                    '${value.filename} · ${(value.size! / 1024 / 1024).toStringAsFixed(1)} MiB',
                  ),
                ],
                if (value.attemptId != null && value.requiresReconciliation)
                  SelectableText('核对编号：${value.attemptId}'),
              ],
              if (error != null) ...[
                const SizedBox(height: 12),
                Semantics(
                  liveRegion: true,
                  child: Text(
                    error!,
                    style: TextStyle(
                      color: Theme.of(context).colorScheme.error,
                    ),
                  ),
                ),
              ],
            ],
          ),
        ),
        actions: [
          TextButton(
            onPressed: pending || confirming
                ? null
                : () => Navigator.pop(context),
            child: const Text('关闭'),
          ),
          TextButton(
            onPressed: pending || confirming
                ? null
                : () => act(UpdateAction.status),
            child: const Text('刷新状态'),
          ),
          if (canAct) ...[
            TextButton(
              onPressed: pending || confirming
                  ? null
                  : () => act(UpdateAction.check),
              child: const Text('检查更新'),
            ),
            if (value.state == UpdateState.available)
              FilledButton(
                onPressed: pending || confirming
                    ? null
                    : () => act(UpdateAction.stage),
                child: const Text('下载并校验'),
              ),
            if (value.state == UpdateState.staged)
              FilledButton(
                onPressed: pending || confirming ? null : confirmInstall,
                child: const Text('安装更新'),
              ),
          ],
        ],
      ),
    );
  }
}
