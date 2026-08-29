// Eklentinin iş mantığının testleri.
//
// İki bölüm var. Saf birim testleri her zaman koşuyor. Bütünleşme
// testleri gerçek bir devboxd'ye bağlanıyor ve yalnız DEVBOX_BIN ortam
// değişkeni verilmişse koşuyor — VS Code'u açmadan, eklentinin çekirdek
// süreçle gerçekten konuşabildiğini gösteriyorlar.

import { spawn, ChildProcess } from 'child_process';
import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { after, before, describe, it } from 'node:test';

import {
  DaemonClient,
  DaemonNotRunning,
  ProjectStatus,
  dataDir,
  humanBytes,
  projectForWorkspace,
  statusText,
  statusTooltip,
} from '../daemon';
import { detectIndent, toggleXdebug, xdebugEnabled } from '../xdebug';

describe('dataDir', () => {
  it('DEVBOX_HOME her şeyin önünde gelir', () => {
    assert.strictEqual(dataDir({ DEVBOX_HOME: '/ozel/yer' }), '/ozel/yer');
  });

  it('DEVBOX_HOME yoksa platforma göre bir yol döner', () => {
    const dir = dataDir({});
    assert.ok(dir.length > 0, 'boş yol döndü');
    assert.ok(path.isAbsolute(dir) || dir === '.devbox', `beklenmedik yol: ${dir}`);
  });
});

describe('projectForWorkspace', () => {
  const projeler = [
    { name: 'magaza', dir: '/kod/magaza' },
    { name: 'blog', dir: '/kod/blog' },
  ] as ProjectStatus[];

  it('dizine göre eşleştirir', () => {
    assert.strictEqual(projectForWorkspace(projeler, '/kod/blog')?.name, 'blog');
  });

  it('sondaki bölü işaretini yok sayar', () => {
    assert.strictEqual(projectForWorkspace(projeler, '/kod/blog/')?.name, 'blog');
  });

  it('kayıtlı olmayan dizin için bir şey döndürmez', () => {
    assert.strictEqual(projectForWorkspace(projeler, '/kod/baska'), undefined);
  });

  // Windows'ta dosya sistemi büyük/küçük harfe duyarsız; Go tarafındaki
  // kayıt da aynı kuralı uyguluyor. İkisi ayrışırsa eklenti projeyi
  // bulamaz.
  it('Windows\'ta büyük/küçük harf ayrımı yapmaz', () => {
    const win = [{ name: 'magaza', dir: 'C:\\Kod\\Magaza' }] as ProjectStatus[];
    assert.strictEqual(projectForWorkspace(win, 'c:\\kod\\magaza', 'win32')?.name, 'magaza');
  });

  it('Unix\'te büyük/küçük harf ayrımı yapar', () => {
    assert.strictEqual(projectForWorkspace(projeler, '/kod/BLOG', 'linux'), undefined);
  });
});

describe('durum çubuğu', () => {
  it('kayıtlı olmayan klasörde de bir şey söyler', () => {
    assert.match(statusText(undefined), /kayıtlı değil/);
    assert.match(statusTooltip(undefined), /devbox project add/);
  });

  it('çalışan ve duran projeyi ayırır', () => {
    const calisan = { name: 'magaza', running: true } as ProjectStatus;
    const duran = { name: 'magaza', running: false } as ProjectStatus;
    assert.notStrictEqual(statusText(calisan), statusText(duran));
    assert.match(statusText(duran), /durdu/);
  });

  it('dizini eksik projeyi uyarı olarak gösterir', () => {
    const eksik = { name: 'magaza', missing: true } as ProjectStatus;
    assert.match(statusText(eksik), /dizin yok/);
  });

  it('ipucunda bellek kullanımını ana süreç diye etiketler', () => {
    const p = { name: 'magaza', running: true, rss: 10 * 1024 * 1024, domain: 'm.test' } as ProjectStatus;
    assert.match(statusTooltip(p), /ana süreç/);
  });
});

describe('humanBytes', () => {
  it('birimleri doğru seçer', () => {
    assert.strictEqual(humanBytes(512), '512 B');
    assert.strictEqual(humanBytes(1024), '1.0 KB');
    assert.strictEqual(humanBytes(10 * 1024 * 1024), '10.0 MB');
  });
});

describe('xdebug anahtarı', () => {
  it('var olan değeri çevirir ve dosyanın gerisine dokunmaz', () => {
    const once = '# yorum\nname: magaza\nphp:\n  version: "8.3"\n  xdebug: false\n';
    const sonra = toggleXdebug(once);
    assert.strictEqual(sonra.enabled, true);
    assert.match(sonra.content, /xdebug: true/);
    assert.match(sonra.content, /# yorum/, 'yorum kayboldu');
    assert.match(sonra.content, /version: "8.3"/, 'diğer ayarlar bozuldu');

    const geri = toggleXdebug(sonra.content);
    assert.strictEqual(geri.enabled, false);
    assert.strictEqual(geri.content, once, 'iki çevirme başa dönmedi');
  });

  it('php bloğu varsa satırı oraya ekler', () => {
    const once = 'name: magaza\nphp:\n  version: "8.3"\n';
    const sonra = toggleXdebug(once);
    assert.strictEqual(sonra.enabled, true);
    assert.match(sonra.content, /php:\n {2}xdebug: true/);
  });

  it('php bloğu yoksa dosyanın sonuna ekler', () => {
    const sonra = toggleXdebug('name: magaza\n');
    assert.strictEqual(sonra.enabled, true);
    assert.ok(xdebugEnabled(sonra.content), 'eklenen satır tanınmıyor');
  });

  it('dosyanın girintisini korur', () => {
    assert.strictEqual(detectIndent('name: a\nphp:\n    version: "8.3"\n'), '    ');
    assert.strictEqual(detectIndent('name: a\n'), '  ');
    const dortlu = toggleXdebug('name: a\nphp:\n    version: "8.3"\n');
    assert.match(dortlu.content, /\n {4}xdebug: true/);
  });

  it('sonunda satır sonu olmayan dosyayı bozmaz', () => {
    const sonra = toggleXdebug('name: magaza');
    assert.match(sonra.content, /^name: magaza\nphp:\n/);
  });
});

describe('çekirdek süreç kapalıyken', () => {
  it('açıklayıcı bir hata verir', () => {
    const bosDizin = fs.mkdtempSync(path.join(os.tmpdir(), 'devbox-test-'));
    const client = new DaemonClient(bosDizin);
    assert.throws(() => client.endpoint(), DaemonNotRunning);
    assert.throws(() => client.token(), DaemonNotRunning);
    try {
      client.endpoint();
    } catch (e) {
      assert.match((e as Error).message, /devbox daemon/, 'hata çözümü söylemiyor');
    }
  });
});

// --- gerçek çekirdek süreçle bütünleşme -------------------------------------

const devboxBin = process.env.DEVBOX_BIN;

describe('gerçek devboxd', { skip: devboxBin ? false : 'DEVBOX_BIN verilmedi' }, () => {
  let home: string;
  let projeDizini: string;
  let daemon: ChildProcess;
  let client: DaemonClient;

  before(async () => {
    home = fs.mkdtempSync(path.join(os.tmpdir(), 'devbox-eklenti-'));
    projeDizini = path.join(home, 'magaza');
    fs.mkdirSync(projeDizini);
    fs.writeFileSync(
      path.join(projeDizini, 'devbox.yaml'),
      'name: magaza\ndomain: magaza.test\nserver: proxy\nproxy: http://127.0.0.1:1\n',
    );

    const env = { ...process.env, DEVBOX_HOME: home };
    await run(devboxBin!, ['project', 'add', '-dir', projeDizini], env);

    daemon = spawn(devboxBin!, ['daemon', '-addr', '127.0.0.1:0'], { env, stdio: 'ignore' });
    client = new DaemonClient(home);

    // Adres dosyası yazılana kadar bekle.
    const son = Date.now() + 15000;
    for (;;) {
      try {
        await client.projects();
        return;
      } catch (e) {
        if (Date.now() > son) {
          throw new Error(`çekirdek süreç açılmadı: ${(e as Error).message}`);
        }
        await new Promise((r) => setTimeout(r, 100));
      }
    }
  });

  after(() => {
    daemon?.kill();
    fs.rmSync(home, { recursive: true, force: true });
  });

  it('projeleri listeler', async () => {
    const projeler = await client.projects();
    assert.strictEqual(projeler.length, 1);
    assert.strictEqual(projeler[0].name, 'magaza');
    assert.strictEqual(projeler[0].serviceName, 'proje-magaza');
    assert.strictEqual(projeler[0].running, false);
  });

  it('açık klasörü projeye eşler', async () => {
    const proje = projectForWorkspace(await client.projects(), projeDizini);
    assert.ok(proje, 'proje bulunamadı');
    assert.strictEqual(proje!.domain, 'magaza.test');
  });

  it('panel adresi jetonu taşır', () => {
    assert.match(client.panelURL(), /^http:\/\/127\.0\.0\.1:\d+\/\?jeton=.+/);
  });

  it('bilinmeyen projeyi açık bir hatayla reddeder', async () => {
    await assert.rejects(() => client.start('yokboyle'), /yokboyle/);
  });
});

/** run, bir komutu çalıştırır ve bitmesini bekler. */
function run(cmd: string, args: string[], env: NodeJS.ProcessEnv): Promise<void> {
  return new Promise((resolve, reject) => {
    const p = spawn(cmd, args, { env, stdio: 'ignore' });
    p.on('error', reject);
    p.on('exit', (code) => (code === 0 ? resolve() : reject(new Error(`${cmd} çıkış kodu ${code}`))));
  });
}
