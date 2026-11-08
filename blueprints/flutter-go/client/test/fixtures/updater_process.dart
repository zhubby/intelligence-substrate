// External-process stand-in for bridge contract tests; no native installer.
import 'dart:convert';
import 'dart:io';

Future<void> main(List<String> args) async {
  final config = jsonDecode(await File(args[1]).readAsString());
  await File(config['log']).writeAsString(
    '${jsonEncode({'args': args, 'hasPath': Platform.environment.containsKey('PATH'), 'hasHome': Platform.environment.containsKey('HOME')})}\n',
    mode: FileMode.append,
  );
  if (config['gate'] != null) {
    final gate = File(config['gate']);
    final deadline = DateTime.now().add(const Duration(seconds: 10));
    while (!await gate.exists()) {
      if (DateTime.now().isAfter(deadline)) exit(9);
      await Future<void>.delayed(const Duration(milliseconds: 10));
    }
  }
  if (config['outputBytes'] > 0) {
    stdout.write('x' * (config['outputBytes'] as int));
  }
  stdout.writeln(jsonEncode(config['snapshot']));
  if (config['exitCode'] != 0) {
    stderr.writeln('fixture failure, not native acceptance');
  }
  await stdout.flush();
  exit(config['exitCode']);
}
