// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package jscodec

// bufferJS is a Node-style Buffer subset for codecs written for runners
// that provide one (ChirpStack's): a Buffer is a Uint8Array with Node's
// constructors, readers, writers and string conversions. Installed when
// Limits.NodeBuffer is set; written from Node's documented API, no code
// taken. Sizes go through the sandbox's bounded Uint8Array.
const bufferJS = `(function (g) {
  'use strict';
  var U8 = g.Uint8Array;
  function Buffer() { throw new TypeError('use Buffer.from or Buffer.alloc'); }
  Buffer.prototype = Object.create(U8.prototype);
  Buffer.prototype.constructor = Buffer;
  function make(n) { var b = new U8(n); Object.setPrototypeOf(b, Buffer.prototype); return b; }
  function wrap(u8) { var b = make(u8.length); for (var i = 0; i < u8.length; i++) b[i] = u8[i]; return b; }
  var B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  function fromHex(s) {
    s = String(s); var n = s.length >> 1, b = make(n);
    for (var i = 0; i < n; i++) { var v = parseInt(s.substr(2 * i, 2), 16); if (isNaN(v)) return b.subarray(0, i); b[i] = v; }
    return b;
  }
  function fromB64(s) {
    s = String(s).replace(/[^A-Za-z0-9+/\-_]/g, '').replace(/-/g, '+').replace(/_/g, '/');
    var out = [], bits = 0, acc = 0;
    for (var i = 0; i < s.length; i++) {
      acc = (acc << 6) | B64.indexOf(s[i]); bits += 6;
      if (bits >= 8) { bits -= 8; out.push((acc >> bits) & 0xFF); }
    }
    return wrap(out);
  }
  function fromUTF8(s) {
    s = String(s); var out = [];
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i);
      if (c >= 0xD800 && c < 0xDC00 && i + 1 < s.length) { var d = s.charCodeAt(i + 1); if (d >= 0xDC00 && d < 0xE000) { c = 0x10000 + ((c - 0xD800) << 10) + (d - 0xDC00); i++; } }
      if (c < 0x80) out.push(c);
      else if (c < 0x800) out.push(0xC0 | c >> 6, 0x80 | c & 63);
      else if (c < 0x10000) out.push(0xE0 | c >> 12, 0x80 | c >> 6 & 63, 0x80 | c & 63);
      else out.push(0xF0 | c >> 18, 0x80 | c >> 12 & 63, 0x80 | c >> 6 & 63, 0x80 | c & 63);
    }
    return wrap(out);
  }
  Buffer.from = function (v, enc) {
    if (typeof v === 'string') {
      enc = (enc || 'utf8').toLowerCase();
      if (enc === 'hex') return fromHex(v);
      if (enc === 'base64' || enc === 'base64url') return fromB64(v);
      if (enc === 'ascii' || enc === 'latin1' || enc === 'binary') { var b = make(v.length); for (var i = 0; i < v.length; i++) b[i] = v.charCodeAt(i) & 0xFF; return b; }
      return fromUTF8(v);
    }
    if (v instanceof g.ArrayBuffer) return wrap(new U8(v));
    if (v && typeof v.length === 'number') return wrap(v);
    throw new TypeError('Buffer.from needs a string, an array or an ArrayBuffer');
  };
  Buffer.alloc = function (n, fill) { var b = make(n); if (fill !== undefined) b.fill(typeof fill === 'number' ? fill : 0); return b; };
  Buffer.allocUnsafe = function (n) { return make(n); };
  Buffer.isBuffer = function (v) { return v instanceof Buffer; };
  Buffer.byteLength = function (s, enc) { return typeof s === 'string' ? Buffer.from(s, enc).length : s.length; };
  Buffer.concat = function (list, total) {
    var n = 0, i; for (i = 0; i < list.length; i++) n += list[i].length;
    if (total !== undefined) n = Math.min(n, total);
    var b = make(n), o = 0;
    for (i = 0; i < list.length && o < n; i++) for (var j = 0; j < list[i].length && o < n; j++) b[o++] = list[i][j];
    return b;
  };
  var P = Buffer.prototype;
  function view(b) { return new g.DataView(b.buffer, b.byteOffset, b.byteLength); }
  function rng(b, o, n) { o = o === undefined ? 0 : o; if (o < 0 || o + n > b.length) throw new RangeError('offset ' + o + ' is out of range'); return o; }
  [['UInt8', 1, 'getUint8', 'setUint8'], ['Int8', 1, 'getInt8', 'setInt8']].forEach(function (d) {
    P['read' + d[0]] = function (o) { return view(this)[d[2]](rng(this, o, d[1])); };
    P['write' + d[0]] = function (v, o) { o = rng(this, o, d[1]); view(this)[d[3]](o, v); return o + d[1]; };
  });
  [['UInt16', 2, 'Uint16'], ['Int16', 2, 'Int16'], ['UInt32', 4, 'Uint32'], ['Int32', 4, 'Int32'],
   ['Float', 4, 'Float32'], ['Double', 8, 'Float64'], ['BigUInt64', 8, 'BigUint64'], ['BigInt64', 8, 'BigInt64']].forEach(function (d) {
    ['LE', 'BE'].forEach(function (e) {
      var le = e === 'LE';
      P['read' + d[0] + e] = function (o) { return view(this)['get' + d[2]](rng(this, o, d[1]), le); };
      P['write' + d[0] + e] = function (v, o) { o = rng(this, o, d[1]); view(this)['set' + d[2]](o, v, le); return o + d[1]; };
    });
  });
  P.readUint8 = P.readUInt8; P.readUint16LE = P.readUInt16LE; P.readUint16BE = P.readUInt16BE;
  P.readUint32LE = P.readUInt32LE; P.readUint32BE = P.readUInt32BE;
  P.readUIntBE = function (o, n) { var v = 0; for (var i = 0; i < n; i++) v = v * 256 + this[rng(this, o, n) + i]; return v; };
  P.readUIntLE = function (o, n) { var v = 0; for (var i = n - 1; i >= 0; i--) v = v * 256 + this[rng(this, o, n) + i]; return v; };
  P.readIntBE = function (o, n) { var v = this.readUIntBE(o, n); return v >= Math.pow(2, 8 * n - 1) ? v - Math.pow(2, 8 * n) : v; };
  P.readIntLE = function (o, n) { var v = this.readUIntLE(o, n); return v >= Math.pow(2, 8 * n - 1) ? v - Math.pow(2, 8 * n) : v; };
  P.toString = function (enc, start, end) {
    var b = this.subarray(start || 0, end === undefined ? this.length : end), i, s = '';
    enc = (enc || 'utf8').toLowerCase();
    if (enc === 'hex') { for (i = 0; i < b.length; i++) s += (b[i] < 16 ? '0' : '') + b[i].toString(16); return s; }
    if (enc === 'base64' || enc === 'base64url') {
      for (i = 0; i < b.length; i += 3) {
        var n = (b[i] << 16) | ((b[i + 1] || 0) << 8) | (b[i + 2] || 0);
        s += B64[n >> 18 & 63] + B64[n >> 12 & 63] + (i + 1 < b.length ? B64[n >> 6 & 63] : '=') + (i + 2 < b.length ? B64[n & 63] : '=');
      }
      return enc === 'base64url' ? s.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : s;
    }
    if (enc === 'ascii' || enc === 'latin1' || enc === 'binary') { for (i = 0; i < b.length; i++) s += String.fromCharCode(enc === 'ascii' ? b[i] & 0x7F : b[i]); return s; }
    for (i = 0; i < b.length; i++) {
      var c = b[i];
      if (c >= 0xF0 && i + 3 < b.length) { c = ((c & 7) << 18) | ((b[++i] & 63) << 12) | ((b[++i] & 63) << 6) | (b[++i] & 63); c -= 0x10000; s += String.fromCharCode(0xD800 + (c >> 10), 0xDC00 + (c & 1023)); continue; }
      if (c >= 0xE0 && i + 2 < b.length) c = ((c & 15) << 12) | ((b[++i] & 63) << 6) | (b[++i] & 63);
      else if (c >= 0xC0 && i + 1 < b.length) c = ((c & 31) << 6) | (b[++i] & 63);
      s += String.fromCharCode(c);
    }
    return s;
  };
  P.toJSON = function () { return { type: 'Buffer', data: Array.prototype.slice.call(this) }; };
  P.equals = function (o) { if (this.length !== o.length) return false; for (var i = 0; i < this.length; i++) if (this[i] !== o[i]) return false; return true; };
  P.slice = P.subarray = function (s, e) { var u = U8.prototype.subarray.call(this, s, e); Object.setPrototypeOf(u, Buffer.prototype); return u; };
  P.copy = function (t, ts, ss, se) { ts = ts || 0; ss = ss || 0; se = se === undefined ? this.length : se; var n = 0; for (var i = ss; i < se && ts + n < t.length; i++, n++) t[ts + n] = this[i]; return n; };
  g.Buffer = Buffer;
})(this)`
