// A dependency-free PostgreSQL client used by Norn tests to prove that the
// connection value Norn delivers works for an ordinary Node process. It reads
// the connection the way node-postgres (pg-connection-string) does: a
// postgresql:// URL parsed by the WHATWG URL parser, percent-decoded user,
// password and database, and query parameters (host, port, sslmode)
// overriding the authority. It speaks the v3 wire protocol with
// SCRAM-SHA-256, MD5 or cleartext authentication. It is not node-postgres.
//
// Usage: node client.mjs value|file "<sql>"
//   value: connection URL from DATABASE_URL
//   file:  connection URL read from the file named by DATABASE_URL_FILE
// Prints each result row as tab-separated text. On a server error prints
// only the SQLSTATE and exits 1.
import crypto from 'node:crypto';
import fs from 'node:fs';
import net from 'node:net';

const [mode, sql] = process.argv.slice(2);
const raw = mode === 'file'
  ? fs.readFileSync(process.env.DATABASE_URL_FILE, 'utf8').trim()
  : process.env.DATABASE_URL;
if (!raw) {
  console.error('no connection value in the environment');
  process.exit(2);
}
const url = new URL(raw);
if (url.protocol !== 'postgresql:' && url.protocol !== 'postgres:') {
  console.error('not a postgresql URL');
  process.exit(2);
}
const params = url.searchParams;
const authorityHost = url.hostname.replace(/^\[(.*)\]$/, '$1');
const host = params.get('host') ?? decodeURIComponent(authorityHost);
const port = Number(params.get('port') ?? (url.port || 5432));
const user = decodeURIComponent(url.username);
const password = decodeURIComponent(url.password);
const database = decodeURIComponent(url.pathname.slice(1));
if ((params.get('sslmode') ?? 'prefer') !== 'disable') {
  console.error('this test client only speaks plaintext (sslmode=disable)');
  process.exit(2);
}

const socket = host.startsWith('/')
  ? net.connect({ path: `${host}/.s.PGSQL.${port}` })
  : net.connect({ host, port });

function message(type, body) {
  const header = Buffer.alloc(type ? 5 : 4);
  let offset = 0;
  if (type) { header.write(type, 0, 'latin1'); offset = 1; }
  header.writeInt32BE(body.length + 4, offset);
  return Buffer.concat([header, body]);
}
const cstring = (value) => Buffer.concat([Buffer.from(value, 'utf8'), Buffer.from([0])]);

const startup = Buffer.alloc(4);
startup.writeInt32BE(196608, 0);
socket.write(message('', Buffer.concat([startup, cstring('user'), cstring(user), cstring('database'), cstring(database), Buffer.from([0])])));

const hmac = (key, data) => crypto.createHmac('sha256', key).update(data).digest();
const sha256 = (data) => crypto.createHash('sha256').update(data).digest();
let scram = null;
const rows = [];
let queried = false;
let pending = Buffer.alloc(0);

function fail(text, code = 1) {
  console.error(text);
  socket.destroy();
  process.exit(code);
}

function handle(type, body) {
  switch (type) {
    case 'R': {
      const code = body.readInt32BE(0);
      if (code === 0) return;
      if (code === 3) return socket.write(message('p', cstring(password)));
      if (code === 5) {
        const inner = crypto.createHash('md5').update(password + user).digest('hex');
        const outer = crypto.createHash('md5').update(Buffer.concat([Buffer.from(inner), body.subarray(4, 8)])).digest('hex');
        return socket.write(message('p', cstring('md5' + outer)));
      }
      if (code === 10) {
        const mechanisms = body.subarray(4).toString('utf8').split('\0');
        if (!mechanisms.includes('SCRAM-SHA-256')) fail('server offers no SCRAM-SHA-256');
        const nonce = crypto.randomBytes(18).toString('base64');
        scram = { nonce, clientFirstBare: `n=,r=${nonce}` };
        const first = Buffer.from('n,,' + scram.clientFirstBare, 'utf8');
        const length = Buffer.alloc(4);
        length.writeInt32BE(first.length, 0);
        return socket.write(message('p', Buffer.concat([cstring('SCRAM-SHA-256'), length, first])));
      }
      if (code === 11) {
        const serverFirst = body.subarray(4).toString('utf8');
        const fields = Object.fromEntries(serverFirst.split(',').map((part) => [part[0], part.slice(2)]));
        if (!fields.r.startsWith(scram.nonce)) fail('SCRAM nonce mismatch');
        const salted = crypto.pbkdf2Sync(Buffer.from(password.normalize('NFKC'), 'utf8'), Buffer.from(fields.s, 'base64'), Number(fields.i), 32, 'sha256');
        const clientKey = hmac(salted, 'Client Key');
        const withoutProof = `c=biws,r=${fields.r}`;
        const authMessage = `${scram.clientFirstBare},${serverFirst},${withoutProof}`;
        const signature = hmac(sha256(clientKey), authMessage);
        const proof = Buffer.from(clientKey.map((byte, index) => byte ^ signature[index]));
        scram.serverSignature = hmac(hmac(salted, 'Server Key'), authMessage).toString('base64');
        return socket.write(message('p', Buffer.from(`${withoutProof},p=${proof.toString('base64')}`, 'utf8')));
      }
      if (code === 12) {
        if (body.subarray(4).toString('utf8') !== `v=${scram.serverSignature}`) fail('SCRAM server signature mismatch');
        return;
      }
      return fail(`unsupported authentication request ${code}`);
    }
    case 'E': {
      let sqlstate = 'unknown';
      for (let offset = 0; offset < body.length && body[offset] !== 0;) {
        const field = String.fromCharCode(body[offset]);
        const end = body.indexOf(0, offset + 1);
        if (field === 'C') sqlstate = body.subarray(offset + 1, end).toString('utf8');
        offset = end + 1;
      }
      return fail(`SQLSTATE ${sqlstate}`);
    }
    case 'D': {
      const columns = body.readInt16BE(0);
      const values = [];
      for (let offset = 2, index = 0; index < columns; index++) {
        const length = body.readInt32BE(offset);
        offset += 4;
        values.push(length < 0 ? '' : body.subarray(offset, offset + length).toString('utf8'));
        offset += Math.max(length, 0);
      }
      return rows.push(values.join('\t'));
    }
    case 'Z':
      if (!queried) {
        queried = true;
        return socket.write(message('Q', cstring(sql)));
      }
      for (const row of rows) console.log(row);
      socket.end(message('X', Buffer.alloc(0)));
      return;
    default:
      return;
  }
}

socket.on('data', (chunk) => {
  pending = Buffer.concat([pending, chunk]);
  while (pending.length >= 5) {
    const length = pending.readInt32BE(1);
    if (pending.length < length + 1) break;
    const type = String.fromCharCode(pending[0]);
    const body = pending.subarray(5, length + 1);
    pending = pending.subarray(length + 1);
    handle(type, body);
  }
});
socket.on('error', (error) => fail(`connection failed: ${error.code ?? 'error'}`));
