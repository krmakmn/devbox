// DevBox çekirdek süreciyle konuşan katman.
//
// Bu dosya bilerek VS Code API'sinden bağımsız: eklentinin iş mantığı
// burada, editör yapıştırması extension.ts'te. Böylece mantık VS Code
// açmadan, düz node ile ve gerçek bir devboxd'ye karşı sınanabiliyor —
// bu depoda kural, yazılan her şeyin çalıştığının gösterilmesi.

import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

/** Projenin çekirdek süreçten okunan durumu. */
export interface ProjectStatus {
  name: string;
  dir: string;
  domain: string;
  server: string;
  running: boolean;
  state?: string;
  pid?: number;
  restarts?: number;
  serviceName: string;
  missing?: boolean;
  error?: string;
  rss?: number;
  cpuSeconds?: number;
}

/**
 * dataDir, DevBox'ın veri dizinini bulur.
 *
 * Go tarafındaki internal/paths.DataDir ile birebir aynı kuralları
 * uyguluyor; ikisi ayrışırsa eklenti jetonu bulamaz ve "çekirdek süreç
 * çalışmıyor" der. Bir test bu ikizliği DEVBOX_HOME üzerinden
 * doğruluyor.
 */
export function dataDir(env: NodeJS.ProcessEnv = process.env): string {
  if (env.DEVBOX_HOME) {
    return env.DEVBOX_HOME;
  }
  if (process.platform === 'win32') {
    if (env.LOCALAPPDATA) {
      return path.join(env.LOCALAPPDATA, 'DevBox');
    }
  } else if (process.platform === 'darwin') {
    return path.join(os.homedir(), 'Library', 'Application Support', 'DevBox');
  } else {
    if (env.XDG_DATA_HOME) {
      return path.join(env.XDG_DATA_HOME, 'devbox');
    }
    return path.join(os.homedir(), '.local', 'share', 'devbox');
  }
  return '.devbox';
}

/** Çekirdek sürecin çalışmadığını anlatan hata. */
export class DaemonNotRunning extends Error {
  constructor(public readonly reason: string) {
    super(`DevBox çekirdek süreci çalışmıyor (${reason}). Başlatmak için: devbox daemon`);
    this.name = 'DaemonNotRunning';
  }
}

/** Çekirdek süreçle konuşan istemci. */
export class DaemonClient {
  constructor(private readonly home: string = dataDir()) {}

  /** endpoint, çekirdek sürecin dinlediği adres. */
  endpoint(): string {
    let raw: string;
    try {
      raw = fs.readFileSync(path.join(this.home, 'api.endpoint'), 'utf8');
    } catch {
      throw new DaemonNotRunning('adres dosyası yok');
    }
    const addr = raw.trim();
    if (!addr) {
      throw new DaemonNotRunning('adres dosyası boş');
    }
    return addr;
  }

  /** token, API jetonu. */
  token(): string {
    let raw: string;
    try {
      raw = fs.readFileSync(path.join(this.home, 'api.token'), 'utf8');
    } catch {
      throw new DaemonNotRunning('jeton dosyası yok');
    }
    const token = raw.trim();
    if (!token) {
      throw new DaemonNotRunning('jeton dosyası boş');
    }
    return token;
  }

  /** baseURL, API'nin kök adresi. */
  baseURL(): string {
    return `http://${this.endpoint()}`;
  }

  /** panelURL, denetim panelinin oturum açan adresi. */
  panelURL(): string {
    return `${this.baseURL()}/?jeton=${encodeURIComponent(this.token())}`;
  }

  private async request<T>(pathname: string, method = 'GET'): Promise<T> {
    const url = `${this.baseURL()}${pathname}`;
    let resp: Response;
    try {
      resp = await fetch(url, {
        method,
        headers: {
          Authorization: `Bearer ${this.token()}`,
          // Yerel API yalnız loopback Host kabul ediyor; fetch zaten
          // adresten doğru Host'u kuruyor, burada açıkça belirtmiyoruz.
        },
      });
    } catch (e) {
      throw new DaemonNotRunning((e as Error).message);
    }
    if (!resp.ok) {
      let message = resp.statusText;
      try {
        const body = (await resp.json()) as { error?: string };
        if (body.error) {
          message = body.error;
        }
      } catch {
        // Gövde JSON değilse durum metniyle yetin.
      }
      throw new Error(`DevBox API ${resp.status}: ${message}`);
    }
    return (await resp.json()) as T;
  }

  /** projects, kayıtlı projelerin durumunu döner. */
  projects(): Promise<ProjectStatus[]> {
    return this.request<ProjectStatus[]>('/v1/projects');
  }

  /** start, projeyi başlatır. */
  start(name: string): Promise<ProjectStatus> {
    return this.request<ProjectStatus>(`/v1/projects/${encodeURIComponent(name)}/start`, 'POST');
  }

  /** stop, projeyi durdurur. */
  stop(name: string): Promise<ProjectStatus> {
    return this.request<ProjectStatus>(`/v1/projects/${encodeURIComponent(name)}/stop`, 'POST');
  }

  /** logs, projenin günlüğünü düz metin olarak döner. */
  async logs(serviceName: string): Promise<string> {
    const url = `${this.baseURL()}/v1/services/${encodeURIComponent(serviceName)}/logs`;
    let resp: Response;
    try {
      resp = await fetch(url, { headers: { Authorization: `Bearer ${this.token()}` } });
    } catch (e) {
      throw new DaemonNotRunning((e as Error).message);
    }
    if (!resp.ok) {
      throw new Error(`DevBox API ${resp.status}: ${resp.statusText}`);
    }
    return await resp.text();
  }
}

/**
 * projectForWorkspace, açık klasöre karşılık gelen projeyi bulur.
 *
 * Eşleştirme dizin yoluna göre: aynı adı taşıyan iki proje olabilir ama
 * aynı dizinde iki proje olamaz. Windows'ta büyük/küçük harf ayrımı
 * olmadığı için karşılaştırma ona göre yapılıyor — Go tarafındaki kayıt
 * da aynı kuralı uyguluyor.
 */
export function projectForWorkspace(
  projects: ProjectStatus[],
  workspaceDir: string,
  platform: string = process.platform,
): ProjectStatus | undefined {
  const normalize = (p: string): string => {
    const cleaned = path.normalize(p).replace(/[\\/]+$/, '');
    return platform === 'win32' ? cleaned.toLowerCase() : cleaned;
  };
  const target = normalize(workspaceDir);
  return projects.find((p) => normalize(p.dir) === target);
}

/**
 * statusText, durum çubuğunda görünecek metni üretir.
 *
 * Proje kayıtlı değilse de bir şey yazıyor: sessiz kalmak, kullanıcının
 * eklentinin çalışıp çalışmadığını anlayamaması demek.
 */
export function statusText(project: ProjectStatus | undefined): string {
  if (!project) {
    return '$(circle-slash) DevBox: kayıtlı değil';
  }
  if (project.missing) {
    return `$(warning) DevBox: ${project.name} (dizin yok)`;
  }
  if (project.running) {
    return `$(check) DevBox: ${project.name}`;
  }
  return `$(debug-stop) DevBox: ${project.name} durdu`;
}

/** statusTooltip, durum çubuğunun ayrıntılı açıklaması. */
export function statusTooltip(project: ProjectStatus | undefined): string {
  if (!project) {
    return 'Bu klasör DevBox kaydında yok.\nEklemek için: devbox project add';
  }
  const lines = [`Proje: ${project.name}`, `Alan adı: https://${project.domain}`,
    `Sunucu: ${project.server}`];
  if (project.running) {
    lines.push(`Durum: çalışıyor${project.pid ? ` (pid ${project.pid})` : ''}`);
    if (project.rss) {
      lines.push(`Bellek: ${humanBytes(project.rss)} (ana süreç)`);
    }
  } else {
    lines.push('Durum: durdu');
  }
  if (project.restarts) {
    lines.push(`Yeniden başlatma: ${project.restarts}`);
  }
  if (project.error) {
    lines.push(`Hata: ${project.error}`);
  }
  return lines.join('\n');
}

/** humanBytes, bayt sayısını okunur biçime çevirir. */
export function humanBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  const units = ['KB', 'MB', 'GB'];
  let value = bytes / 1024;
  let i = 0;
  while (value >= 1024 && i < units.length - 1) {
    value /= 1024;
    i++;
  }
  return `${value.toFixed(1)} ${units[i]}`;
}
