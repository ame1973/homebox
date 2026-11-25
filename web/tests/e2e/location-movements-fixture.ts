import { DatabaseSync } from 'node:sqlite'
import { expect } from './auth-fixture.ts'

export const movementRoot = 'screen-movement-root'
export const historicalParent = 'old/parent\0😀'
export const deletedMovementOwner = 'retained/deleted\0\r\n😀'
export const firstMovement = '\0event‮first'
export const maximumMovement = '😀'.repeat(128)
export const legacyOccurrence = 'legacy\r\n時間'

// Development-only retained sources are created while the real server is stopped.
export function prepareMovementScreen(database: string) {
  const db = new DatabaseSync(database), now = '2026-11-06T00:00:00Z'
  try {
    db.exec('PRAGMA foreign_keys=ON; BEGIN IMMEDIATE')
    const addLocation = db.prepare('INSERT INTO locations(id,parent_id,code,name,kind,status,created_at,updated_at,deleted_at) VALUES(?,?,?,?,?,?,?,?,?)')
    const counter = db.prepare('INSERT INTO projection_revisions(entity_type,entity_id,revision) VALUES(?,?,1)')
    const current = db.prepare('INSERT INTO current_projections(entity_type,entity_id,entity_revision,payload_json,updated_at) VALUES(?,?,1,?,?)')
    for (const [id, code, name, deleted] of [[movementRoot, 'MOVE-ROOT', '變動房間', false], [historicalParent, 'MOVE-PARENT', '已刪除的舊上層', true], [deletedMovementOwner, 'MOVE-DELETED', '已刪除的位置', true]] as const) {
      addLocation.run(id, null, code, name, 'box', 'active', now, now, deleted ? now : null)
      counter.run('location', id)
      if (!deleted) current.run('location', id, JSON.stringify({ id, parent_id: null, code, name, kind: 'box', status: 'active', sort_order: 0, description: '' }), now)
    }
    const addMovement = db.prepare('INSERT INTO location_events(id,location_id,occurred_at,from_parent_id,to_parent_id) VALUES(?,?,?,?,?)')
    for (const id of [firstMovement, ...Array.from({ length: 21 }, (_, index) => `movement-${String(index).padStart(2, '0')}`), maximumMovement]) {
      addMovement.run(id, movementRoot, legacyOccurrence, historicalParent, null)
      counter.run('location_event', id)
    }
    for (const id of ['deleted-event-1', 'deleted-event-2']) {
      addMovement.run(id, deletedMovementOwner, legacyOccurrence, historicalParent, null)
      counter.run('location_event', id)
    }
    db.exec('COMMIT')
    expect(db.prepare('PRAGMA foreign_key_check').all()).toEqual([])
  } finally { db.close() }
}
