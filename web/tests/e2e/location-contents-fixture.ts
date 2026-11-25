import { DatabaseSync } from 'node:sqlite'
import { expect } from './auth-fixture.ts'

export const contentsRoot = 'screen-contents-root'
export const contentsChild = 'new'
export const contentsUnsafeLocation = 'unsafe/location\0😀'
export const contentsUnsafeUnit = '\0legacy/unit😀'
export const contentsUnsafeItem = 'item/legacy\0'

// Development-only retained sources are created while the real server is stopped.
export function prepareContentsScreen(database: string) {
  const db = new DatabaseSync(database), now = '2026-11-06T00:00:00Z'
  try {
    db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE')
    const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)')
    const current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,1,?,?)')
    const baseline = (kind: string, id: string, payload: object) => { counter.run(kind, id); current.run(kind, id, JSON.stringify(payload), now) }
    const addLocation = db.prepare('INSERT INTO locations(id,parent_id,code,name,kind,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)')
    for (const [id, parent, code, name] of [[contentsRoot, null, 'SCREEN-ROOT', '內容房間'], [contentsChild, contentsRoot, 'SCREEN-CHILD', '內容箱'], [contentsUnsafeLocation, contentsRoot, 'SCREEN-LEGACY', '舊位置']] as const) {
      addLocation.run(id, parent, code, name, 'box', 'active', now, now)
      baseline('location', id, { id, parent_id: parent, code, name, kind: 'box', status: 'active', sort_order: 0, description: '' })
    }
    const addItem = db.prepare('INSERT INTO items(id,name,tracking_mode,created_at,updated_at) VALUES(?,?,?,?,?)')
    for (const [id, mode] of [['screen-individual', 'individual'], ['screen-quantity', 'quantity'], [contentsUnsafeItem, 'quantity']] as const) {
      addItem.run(id, id, mode, now, now)
      baseline('item', id, { id, name: id, brand: null, model: null, category_id: null, description: '', notes: '', tracking_mode: mode, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active' })
      counter.run('search_doc', JSON.stringify(['item', id]))
    }
    const addUnit = db.prepare('INSERT INTO item_units(id,item_id,location_id,asset_code,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?)')
    for (let index = 0; index < 21; index++) {
      const id = index === 0 ? contentsUnsafeUnit : `screen-unit-${String(index).padStart(2, '0')}`
      const location = index % 2 === 0 ? contentsRoot : contentsChild
      const code = index === 0 ? 'ASSET-‮LEGACY' : `ASSET-${String(index).padStart(2, '0')}`
      const status = index === 0 ? 'disposed' : 'stored'
      addUnit.run(id, 'screen-individual', location, code, status, now, now)
      baseline('unit', id, { id, item_id: 'screen-individual', location_id: location, asset_code: code, serial_number: null, notes: '', status })
    }
    const addStock = db.prepare('INSERT INTO stock_balances(item_id,location_id,quantity_base,updated_at) VALUES(?,?,?,?)')
    for (const [item, location, quantity] of [[contentsUnsafeItem, contentsRoot, 0], ['screen-quantity', contentsChild, Number.MAX_SAFE_INTEGER]] as const) {
      addStock.run(item, location, quantity, now)
      baseline('stock', JSON.stringify([item, location]), { item_id: item, location_id: location, quantity_base: quantity, unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1 })
    }
    db.exec('COMMIT')
    expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
  } finally { db.close() }
}
