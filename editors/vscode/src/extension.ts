// VS Code yapıştırması. İş mantığı daemon.ts ve xdebug.ts içinde; burada
// yalnız komutlar, durum çubuğu ve dosya işlemleri var.

import * as fs from 'fs/promises';
import * as path from 'path';
import * as vscode from 'vscode';

import { DaemonClient, DaemonNotRunning, ProjectStatus, projectForWorkspace, statusText, statusTooltip } from './daemon';
import { toggleXdebug, xdebugEnabled } from './xdebug';

let statusBar: vscode.StatusBarItem;
let timer: NodeJS.Timeout | undefined;
let output: vscode.OutputChannel;

export function activate(context: vscode.ExtensionContext): void {
  output = vscode.window.createOutputChannel('DevBox');
  statusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 100);
  statusBar.command = 'devbox.openPanel';
  statusBar.show();
  context.subscriptions.push(statusBar, output);

  const komutlar: Array<[string, () => Promise<void>]> = [
    ['devbox.up', () => withProject(async (client, project) => {
      await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Notification, title: `${project.name} başlatılıyor…` },
        () => client.start(project.name),
      );
      vscode.window.showInformationMessage(`DevBox: ${project.name} çalışıyor — https://${project.domain}`);
    })],
    ['devbox.down', () => withProject(async (client, project) => {
      await client.stop(project.name);
      vscode.window.showInformationMessage(`DevBox: ${project.name} durduruldu`);
    })],
    ['devbox.openSite', () => withProject(async (_client, project) => {
      await vscode.env.openExternal(vscode.Uri.parse(`https://${project.domain}`));
    })],
    ['devbox.openMailbox', () => withProject(async (_client, project) => {
      await vscode.env.openExternal(vscode.Uri.parse(`https://mail.${project.domain}`));
    })],
    ['devbox.openPanel', async () => {
      try {
        await vscode.env.openExternal(vscode.Uri.parse(new DaemonClient().panelURL()));
      } catch (e) {
        showError(e);
      }
    }],
    ['devbox.showLogs', () => withProject(async (client, project) => {
      const text = await client.logs(project.serviceName);
      output.clear();
      output.append(text);
      output.show(true);
    })],
    ['devbox.toggleXdebug', toggleXdebugCommand],
  ];
  for (const [name, handler] of komutlar) {
    context.subscriptions.push(vscode.commands.registerCommand(name, handler));
  }

  refresh();
  scheduleRefresh(context);
}

export function deactivate(): void {
  if (timer) {
    clearInterval(timer);
  }
}

function scheduleRefresh(context: vscode.ExtensionContext): void {
  const seconds = Math.max(2, vscode.workspace.getConfiguration('devbox').get<number>('refreshInterval', 5));
  timer = setInterval(refresh, seconds * 1000);
  context.subscriptions.push({ dispose: () => timer && clearInterval(timer) });
}

/** currentProject, açık klasöre karşılık gelen projeyi bulur. */
async function currentProject(client: DaemonClient): Promise<ProjectStatus | undefined> {
  const folder = vscode.workspace.workspaceFolders?.[0];
  if (!folder) {
    return undefined;
  }
  return projectForWorkspace(await client.projects(), folder.uri.fsPath);
}

async function refresh(): Promise<void> {
  try {
    const project = await currentProject(new DaemonClient());
    statusBar.text = statusText(project);
    statusBar.tooltip = statusTooltip(project);
  } catch (e) {
    // Çekirdek süreç kapalıysa bu bir hata değil; durum çubuğu bunu
    // söylüyor ve kullanıcıyı bildirimle rahatsız etmiyoruz.
    statusBar.text = '$(circle-slash) DevBox: çekirdek kapalı';
    statusBar.tooltip = e instanceof DaemonNotRunning ? e.message : String(e);
  }
}

/** withProject, komutları proje bulma ve hata gösterme ile sarar. */
async function withProject(
  fn: (client: DaemonClient, project: ProjectStatus) => Promise<void>,
): Promise<void> {
  try {
    const client = new DaemonClient();
    const project = await currentProject(client);
    if (!project) {
      const secim = await vscode.window.showWarningMessage(
        'Bu klasör DevBox kaydında yok.', 'Nasıl eklenir?');
      if (secim) {
        vscode.window.showInformationMessage('Proje dizininde: devbox project add');
      }
      return;
    }
    await fn(client, project);
  } catch (e) {
    showError(e);
  } finally {
    refresh();
  }
}

async function toggleXdebugCommand(): Promise<void> {
  const folder = vscode.workspace.workspaceFolders?.[0];
  if (!folder) {
    vscode.window.showWarningMessage('Açık bir klasör yok.');
    return;
  }
  const file = path.join(folder.uri.fsPath, 'devbox.yaml');
  try {
    const content = await fs.readFile(file, 'utf8');
    const edit = toggleXdebug(content);
    await fs.writeFile(file, edit.content, 'utf8');
    vscode.window.showInformationMessage(
      edit.enabled
        ? 'Xdebug açıldı. Etkili olması için projeyi yeniden başlatın.'
        : 'Xdebug kapatıldı. Etkili olması için projeyi yeniden başlatın.');
  } catch (e) {
    showError(e);
  }
}

function showError(e: unknown): void {
  const message = e instanceof Error ? e.message : String(e);
  vscode.window.showErrorMessage(`DevBox: ${message}`);
  output.appendLine(message);
}

// xdebugEnabled dışa aktarılıyor ki durum çubuğu ileride Xdebug'ı da
// gösterebilsin; şimdilik yalnız komut kullanıyor.
export { xdebugEnabled };
