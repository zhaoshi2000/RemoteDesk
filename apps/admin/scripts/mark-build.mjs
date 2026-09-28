import { readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
const root = new URL('../../../internal/server/static/', import.meta.url)
const index = readFileSync(new URL('index.html', root), 'utf8')
if (!index.includes('/admin/assets/')) throw new Error('Expected bundled Vue assets, not a placeholder page')
const assets = readdirSync(new URL('assets/', root))
const hashes = Object.fromEntries(assets.map(name => [name, createHash('sha256').update(readFileSync(new URL(`assets/${name}`, root))).digest('hex')]))
writeFileSync(new URL('build-manifest.json', root), JSON.stringify({ framework: 'Vue 3 + Element Plus', version: '0.3.0', base: '/admin/', assets: hashes }, null, 2))
