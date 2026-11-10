import type { components, operations } from '../src/lib/api/schema'

type Record = components['schemas']['OfflineRecord']

const stock = {
  entity_type: 'stock', entity_id: '["item-1","location-1"]', entity_revision: 1,
  operation: 'upsert', projection_version: 1,
  payload: {
    item_id: 'item-1', location_id: 'location-1', quantity_base: 250,
    base_unit: 'g', unit_code: 'kg', scale_factor: 1000,
  },
} as const satisfies Record

const tombstone = {
  entity_type: 'barcode', entity_id: 'barcode-1', entity_revision: 2,
  operation: 'delete', projection_version: 1, payload: null,
} as const satisfies Record

// @ts-expect-error A tombstone must never carry a payload.
const invalidDelete: Record = { ...tombstone, payload: stock.payload }
// @ts-expect-error Frozen records require an entity revision.
const missingRevision: Record = { entity_type: 'item', entity_id: 'item-1', operation: 'delete', projection_version: 1, payload: null }
// @ts-expect-error Stock quantities cannot be strings.
const invalidQuantity: Record = { ...stock, payload: { ...stock.payload, quantity_base: '250' } }

type AuthHeaders = operations['post_auth_password']['parameters']['header']
const authHeaders = {
  Origin: 'https://homebox.example.internal', 'X-CSRF-Token': 'csrf-fixture',
  'Idempotency-Key': 'explicit-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev',
} as const satisfies AuthHeaders

// @ts-expect-error Protected authentication writes require Origin as well as CSRF.
const noOrigin: AuthHeaders = { 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': 'explicit-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Origin alone is not a session-bound request proof.
const noCSRF: AuthHeaders = { Origin: 'https://homebox.example.internal', 'Idempotency-Key': 'explicit-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Opaque session credentials must never become Admin JSON fields.
const rawCredentialAdmin: components['schemas']['Admin'] = { id: 'admin-1', revision: 1, username: 'owner', must_change_password: false, session_token: 'must-not-be-exposed' }
// @ts-expect-error Login accepts the password, not a caller-supplied database hash.
const hashLogin: components['schemas']['LoginInput'] = { username: 'owner', password_hash: 'must-not-be-accepted' }

type LocationCreateHeaders = operations['post_locations']['parameters']['header']
const childLocationHeaders = { ...authHeaders, 'If-Match': '"3"' } satisfies LocationCreateHeaders
const rootLocationHeaders = { ...authHeaders } satisfies LocationCreateHeaders
// @ts-expect-error Domain writes require the configured browser Origin as well.
const locationWithoutOrigin: LocationCreateHeaders = { 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': 'location-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
const rootLocation = { name: 'Box', code: 'BOX-1', parent_id: null, sort_order: -1 } satisfies components['schemas']['LocationCreate']
const minimalLocation = { name: 'Box', code: 'BOX-1' } satisfies components['schemas']['LocationCreate']
const minimalItem = { name: 'Item' } satisfies components['schemas']['ItemCreate']
const rootMove = { target_parent_id: null } satisfies components['schemas']['LocationMove']
// @ts-expect-error The internal presence wrapper is not the nullable wire field.
const parentWrapper: components['schemas']['LocationWrite'] = { parent_id: { set: true, value: null } }
// @ts-expect-error Sort order is a safe integer on the wire, never text.
const textSortOrder: components['schemas']['LocationCreate'] = { name: 'Box', code: 'BOX-1', sort_order: '1' }
// @ts-expect-error Only the parent field is nullable; kind is not.
const nullKind: components['schemas']['LocationWrite'] = { kind: null }

declare const createLocation: typeof import('../src/lib/api/locations').createLocation
declare const writeContext: import('../src/lib/api/client').WriteContext
const createRoot = createLocation({ name: 'Box', code: 'BOX-1' }, writeContext)
const createChild = createLocation({ name: 'Box', code: 'BOX-2', parent_id: 'parent' }, { ...writeContext, expectedRevision: 3 })
// @ts-expect-error Child creation cannot omit its reviewed parent revision.
const unprovenChild = createLocation({ name: 'Box', code: 'BOX-2', parent_id: 'parent' }, writeContext)
// @ts-expect-error Root creation cannot silently attach an unrelated If-Match.
const unrelatedRootProof = createLocation({ name: 'Box', code: 'BOX-1' }, { ...writeContext, expectedRevision: 3 })

type CategoryCreateHeaders = operations['post_categories']['parameters']['header']
const rootCategoryHeaders = { ...authHeaders } satisfies CategoryCreateHeaders
const childCategoryHeaders = { ...authHeaders, 'If-Match': '"3"' } satisfies CategoryCreateHeaders
const categoryCreate = { name: 'Category' } satisfies components['schemas']['CategoryCreate']
const categoryMove = { parent_id: null } satisfies components['schemas']['CategoryWrite']
const categoryRename = { name: 'Renamed' } satisfies components['schemas']['CategoryWrite']
const category = { id: 'category', name: 'Category', parent_id: null, revision: 1 } satisfies components['schemas']['Category']
const tag = { name: 'Tag' } satisfies components['schemas']['TagWrite']
// @ts-expect-error Creating a category must include a name, unlike a nullable-parent-only patch.
const missingCategoryName: components['schemas']['CategoryCreate'] = { parent_id: null }
// @ts-expect-error Category responses always include the explicit nullable parent.
const missingCategoryParent: components['schemas']['Category'] = { id: 'category', name: 'Category', revision: 1 }
// @ts-expect-error Internal parent presence wrappers are not wire objects.
const categoryParentWrapper: components['schemas']['CategoryWrite'] = { parent_id: { set: true, value: null } }
// @ts-expect-error Classification writes require Origin independently of CSRF.
const missingCategoryOrigin: CategoryCreateHeaders = { 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': 'category-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Tag names are not nullable.
const nullTag: components['schemas']['TagWrite'] = { name: null }

declare const createCategory: typeof import('../src/lib/api/taxonomy').createCategory
declare const createTag: typeof import('../src/lib/api/taxonomy').createTag
declare const patchCategory: typeof import('../src/lib/api/taxonomy').patchCategory
const rootCategory = createCategory({ name: 'Category' }, writeContext)
const childCategory = createCategory({ name: 'Child', parent_id: 'parent' }, { ...writeContext, expectedRevision: 3 })
const createNamedTag = createTag({ name: 'Tag' }, writeContext)
const patchCategoryParent = patchCategory('category', { parent_id: null }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Child categories require the original reviewed parent proof.
const unprovenCategoryChild = createCategory({ name: 'Child', parent_id: 'parent' }, writeContext)
// @ts-expect-error Root categories cannot attach an unrelated aggregate proof.
const unrelatedCategoryProof = createCategory({ name: 'Root' }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Tag creation has no parent/target aggregate precondition.
const unrelatedTagProof = createTag({ name: 'Tag' }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Patches require an original target proof even for root moves.
const unprovenCategoryMove = patchCategory('category', { parent_id: null }, writeContext)

type ItemCreateHeaders = operations['post_items']['parameters']['header']
type ItemPatchHeaders = operations['patch_items_id']['parameters']['header']
const itemHeaders = { ...authHeaders } satisfies ItemCreateHeaders
const itemProof = { ...authHeaders, 'If-Match': '"3"' } satisfies ItemPatchHeaders
const measuredItem = { name: 'Stock', tracking_mode: 'quantity', unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } satisfies components['schemas']['ItemCreate']
const clearItem = { brand: null, model: null, category_id: null } satisfies components['schemas']['ItemWrite']
const restoreItem = { status: 'active' } satisfies components['schemas']['ItemWrite']
const emptyItemPage = { data: [], next_cursor: null } satisfies components['schemas']['ItemList']
const currentItem = {
  id: 'item', revision: 1, name: 'Item', brand: null, model: null, category_id: null,
  description: '', notes: '', tracking_mode: 'individual', unit_code: 'pcs', base_unit: 'pcs', scale_factor: 1, status: 'active',
} satisfies components['schemas']['Item']
// @ts-expect-error Catalog creation requires Origin independently of CSRF.
const itemWithoutOrigin: ItemCreateHeaders = { 'X-CSRF-Token': 'csrf-fixture', 'Idempotency-Key': 'item-fixture-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Catalog editing requires the original aggregate proof.
const itemWithoutProof: ItemPatchHeaders = { ...authHeaders }
// @ts-expect-error Nullable brand/model/category are scalar wire fields, not internal wrappers.
const wrappedItem: components['schemas']['ItemWrite'] = { brand: { set: true, value: null } }
// @ts-expect-error Only brand/model/category are nullable; measurement cannot become null.
const nullItemScale: components['schemas']['ItemWrite'] = { scale_factor: null }
// @ts-expect-error Archived catalog metadata is restored explicitly, not with an invented deleted state.
const deletedItem: components['schemas']['ItemWrite'] = { status: 'deleted' }
// @ts-expect-error Catalog metadata cannot create or mutate holdings implicitly.
const implicitItemStock: components['schemas']['ItemCreate'] = { name: 'Item', quantity_base: 1 }
// @ts-expect-error Every live item page explicitly indicates whether another page exists.
const incompleteItemPage: components['schemas']['ItemList'] = { data: [] }

declare const createCatalogItem: typeof import('../src/lib/api/catalog').createItem
declare const patchCatalogItem: typeof import('../src/lib/api/catalog').patchItem
declare const archiveCatalogItem: typeof import('../src/lib/api/catalog').archiveItem
declare const deleteCatalogItem: typeof import('../src/lib/api/catalog').deleteItem
declare const fetchCatalogPage: typeof import('../src/lib/api/catalog').fetchItemsPage
const createMinimalCatalog = createCatalogItem({ name: 'Item' }, writeContext)
const createDefaultSubset = createCatalogItem({ name: 'Item', unit_code: 'pcs' }, writeContext)
const createMeasuredCatalog = createCatalogItem(measuredItem, writeContext)
const createClassifiedCatalog = createCatalogItem({ name: 'Item', category_id: 'category' }, writeContext)
const patchCatalogNull = patchCatalogItem('item', clearItem, { ...writeContext, expectedRevision: 1 })
const archiveCatalog = archiveCatalogItem('item', {}, { ...writeContext, expectedRevision: 1 })
const deleteCatalog = deleteCatalogItem('item', { ...writeContext, expectedRevision: 1 })
const readFilteredCatalog = fetchCatalogPage({ from: '2026-01-01', to: '2026-12-31', currency: 'TWD', category_id: 'category', location_id: 'location',
  scope: 'subtree', status: 'active', merchant: 'Merchant', q: 'Query', limit: 50 }, new AbortController().signal)
// @ts-expect-error Nondefault measurements need the complete explicit tuple, even though the generator ignores if/then.
const incompleteCatalogUnit = createCatalogItem({ name: 'Item', unit_code: 'kg' }, writeContext)
// @ts-expect-error A nondefault base unit cannot silently inherit display/scale defaults.
const incompleteCatalogBase = createCatalogItem({ name: 'Item', base_unit: 'g' }, writeContext)
// @ts-expect-error A nondefault scale cannot silently infer either unit.
const incompleteCatalogScale = createCatalogItem({ name: 'Item', scale_factor: 1000 }, writeContext)
// @ts-expect-error Item creation never attaches an unrelated parent/target proof, including when a category is selected.
const unrelatedCatalogProof = createCatalogItem({ name: 'Item', category_id: 'category' }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Partial catalog edits require the reviewed original resource proof.
const unprovenCatalogPatch = patchCatalogItem('item', { name: 'New' }, writeContext)
// @ts-expect-error Archival requires the original item proof.
const unprovenCatalogArchive = archiveCatalogItem('item', {}, writeContext)
// @ts-expect-error Deletion requires the original item proof even for historical reconciliation.
const unprovenCatalogDelete = deleteCatalogItem('item', writeContext)
// @ts-expect-error Holdings are not implicit catalog metadata fields.
const implicitCatalogQuantity = createCatalogItem({ name: 'Item', quantity_base: 1 }, writeContext)
// @ts-expect-error The catalog status filter has only contracted metadata states.
const invalidCatalogStatus = fetchCatalogPage({ status: 'disposed' }, new AbortController().signal)

type BarcodeCreateHeaders = operations['post_items_id_barcodes']['parameters']['header']
const ownedCreateHeaders = { ...authHeaders, 'If-Match': '"3"' } satisfies BarcodeCreateHeaders
const barcodeBody = { symbology: 'UPC_A', raw_value: '036000291452' } satisfies components['schemas']['BarcodeWrite']
const aliasBody = { alias: '  Exact é  ' } satisfies components['schemas']['AliasWrite']
const aliasResource = { id: 'alias', item_id: 'item', revision: 1, alias: '  Exact é  ' } satisfies components['schemas']['Alias']
const attributeBody = { attribute_key: 'Specification', attribute_value: '' } satisfies components['schemas']['AttributeWrite']
const attributePartial = { attribute_value: '' } satisfies components['schemas']['AttributePatch']
const tagMembershipPage = { data: [], next_cursor: null } satisfies components['schemas']['ItemTagList']
const clearMembership = { tag_ids: [] } satisfies components['schemas']['ItemTagsInput']
const tagReplacementResult = currentItem satisfies operations['put_items_id_tags']['responses'][200]['content']['application/json']
// @ts-expect-error Owned creation requires exact Origin independently of its reviewed owner proof.
const missingOwnedOrigin: BarcodeCreateHeaders = { 'X-CSRF-Token': 'csrf', 'Idempotency-Key': 'owned-key', 'X-HomeBox-Client-Version': '0.1.0-dev', 'If-Match': '"3"' }
// @ts-expect-error Product barcode digits remain strings, never numeric coercions that lose zeros.
const numericBarcode: components['schemas']['BarcodeWrite'] = { symbology: 'UPC_A', raw_value: 36000291452 }
// @ts-expect-error Authoritative normalization is not a writable field.
const callerNormalizedBarcode: components['schemas']['BarcodeWrite'] = { ...barcodeBody, normalized_value: 'caller' }
// @ts-expect-error Stable alias ownership cannot be reassigned through a write.
const reassignedAlias: components['schemas']['AliasWrite'] = { alias: 'Alias', item_id: 'other' }
// @ts-expect-error Every alias response includes the actual alias text.
const incompleteAlias: components['schemas']['Alias'] = { id: 'alias', item_id: 'item', revision: 1 }
// @ts-expect-error Attribute creation requires an explicit value even when it is empty.
const incompleteAttribute: components['schemas']['AttributeWrite'] = { attribute_key: 'Key' }
// @ts-expect-error Empty attribute text is represented by a string, not null.
const nullAttribute: components['schemas']['AttributePatch'] = { attribute_value: null }
// @ts-expect-error Every item-membership page explicitly terminates or continues.
const incompleteMembershipPage: components['schemas']['ItemTagList'] = { data: [] }
// @ts-expect-error Tag replacement returns the frozen owning Item, not a partial TagList.
const partialMembershipResult: operations['put_items_id_tags']['responses'][200]['content']['application/json'] = { data: [], next_cursor: null }

declare const ownedCatalogAPI: typeof import('../src/lib/api/catalog-links')
const owningItemProof = { ...writeContext, expectedRevision: 7 }
const independentRowProof = { ...writeContext, expectedRevision: 2, itemID: 'item' }
const ownedSDKCalls = [
  ownedCatalogAPI.fetchItemBarcodesPage('item', { limit: 50 }, new AbortController().signal),
  ownedCatalogAPI.fetchItemAliasesPage('item', {}, new AbortController().signal),
  ownedCatalogAPI.fetchItemAttributesPage('item', {}, new AbortController().signal),
  ownedCatalogAPI.fetchItemTagsPage('item', {}, new AbortController().signal),
  ownedCatalogAPI.createBarcode('item', barcodeBody, owningItemProof),
  ownedCatalogAPI.createAlias('item', aliasBody, owningItemProof),
  ownedCatalogAPI.createAttribute('item', attributeBody, owningItemProof),
  ownedCatalogAPI.patchBarcode('barcode', barcodeBody, independentRowProof),
  ownedCatalogAPI.patchAlias('alias', aliasBody, independentRowProof),
  ownedCatalogAPI.patchAttribute('attribute', attributePartial, independentRowProof),
  ownedCatalogAPI.deleteBarcode('barcode', independentRowProof),
  ownedCatalogAPI.deleteAlias('alias', independentRowProof),
  ownedCatalogAPI.deleteAttribute('attribute', independentRowProof),
  ownedCatalogAPI.replaceItemTags('item', clearMembership, owningItemProof),
]
// @ts-expect-error Owned creation requires the reviewed original Item revision.
const unprovenOwnedBarcode = ownedCatalogAPI.createBarcode('item', barcodeBody, writeContext)
// @ts-expect-error Alias creation also requires the Item proof, unlike global Tag creation.
const unprovenOwnedAlias = ownedCatalogAPI.createAlias('item', aliasBody, writeContext)
// @ts-expect-error Attribute creation cannot silently adopt a current Item revision.
const unprovenOwnedAttribute = ownedCatalogAPI.createAttribute('item', attributeBody, writeContext)
// @ts-expect-error Independent row replies must validate the retained original owner too.
const ownerlessBarcodePatch = ownedCatalogAPI.patchBarcode('barcode', barcodeBody, owningItemProof)
// @ts-expect-error A row deletion context is not an unscoped Item-proof context.
const ownerlessAliasDelete = ownedCatalogAPI.deleteAlias('alias', owningItemProof)
// @ts-expect-error Empty attribute patches are rejected at compile time and runtime.
const emptyOwnedAttributePatch = ownedCatalogAPI.patchAttribute('attribute', {}, independentRowProof)
// @ts-expect-error Attribute creation requires a possibly-empty explicit value.
const missingOwnedAttributeValue = ownedCatalogAPI.createAttribute('item', { attribute_key: 'Key' }, owningItemProof)
// @ts-expect-error Authoritative normalization is never a submitted SDK field.
const normalizedOwnedInput = ownedCatalogAPI.createBarcode('item', { ...barcodeBody, normalized_value: 'caller' }, owningItemProof)
// @ts-expect-error Stable owner identity cannot be reassigned through an alias patch.
const reassignedOwnedInput = ownedCatalogAPI.patchAlias('alias', { ...aliasBody, item_id: 'other' }, independentRowProof)
// @ts-expect-error Complete tag replacement needs the original owning Item proof.
const unprovenTagSet = ownedCatalogAPI.replaceItemTags('item', clearMembership, writeContext)
// @ts-expect-error Null is not the explicit empty membership array.
const nullTagSet = ownedCatalogAPI.replaceItemTags('item', { tag_ids: null }, owningItemProof)
// @ts-expect-error Assigned reads accept only their documented paging fields.
const filteredOwnedRead = ownedCatalogAPI.fetchItemTagsPage('item', { q: 'uncontracted' }, new AbortController().signal)
type UnitCreateHeaders = operations['post_items_id_units']['parameters']['header']
type UnitEditHeaders = operations['patch_units_id']['parameters']['header']
type StockMutationHeaders = operations['post_stock_receive']['parameters']['header']
type UnitDeletion = operations['delete_units_id']['responses'][200]['content']['application/json']
const reviewedHoldingItem = { ...authHeaders, 'If-Match': '"7"' } satisfies UnitCreateHeaders & StockMutationHeaders
const reviewedHoldingUnit = { ...authHeaders, 'If-Match': '"3"' } satisfies UnitEditHeaders
const unitWithUnknownLocation = { location_id: null } satisfies components['schemas']['UnitCreate']
const unitWithEmptySerial = { location_id: 'location', serial_number: '', notes: '', status: 'stored' } satisfies components['schemas']['UnitCreate']
const unitSerialClear = { serial_number: null } satisfies components['schemas']['UnitWrite']
const unitNotesClear = { notes: '' } satisfies components['schemas']['UnitWrite']
const unitMove = { target_location_id: 'destination' } satisfies components['schemas']['UnitMove']
const unitStatus = { status: 'lost', note: 'Explicitly reviewed' } satisfies components['schemas']['UnitStatusInput']
const physicalUnitDisposal = { status: 'disposed', disposal_kind: 'discarded', note: 'Explicit non-asset action' } satisfies components['schemas']['UnitStatusInput']
// @ts-expect-error Non-asset disposal must explicitly distinguish its physical action.
const untypedUnitDisposal: components['schemas']['UnitStatusInput'] = { status: 'disposed' }
// @ts-expect-error An operational state cannot silently reclassify historical disposal.
const operationalDisposalKind: components['schemas']['UnitStatusInput'] = { status: 'stored', disposal_kind: 'discarded' }
// @ts-expect-error Financial proceeds require the separate asset lifecycle command.
const automaticUnitProceeds: components['schemas']['UnitStatusInput'] = { status: 'disposed', disposal_kind: 'sold', realized_proceeds_minor: 1 }
// @ts-expect-error New physical disposal requires one of the explicit supported actions.
const unknownUnitDisposal: components['schemas']['UnitStatusInput'] = { status: 'disposed', disposal_kind: 'other' }
void [physicalUnitDisposal, untypedUnitDisposal, operationalDisposalKind, automaticUnitProceeds, unknownUnitDisposal]
const holdingUnit = { id: 'unit', revision: 3, item_id: 'item', location_id: null, asset_code: 'AST-001', serial_number: null, notes: '', status: 'stored' } satisfies components['schemas']['Unit']
const holdingStock = { item_id: 'item', location_id: 'location', revision: 2, quantity_base: 1500, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 } satisfies components['schemas']['Stock']
const receiveStock = { item_id: 'item', target_location_id: 'location', quantity_base: 1000 } satisfies components['schemas']['StockReceiveInput']
const consumeStock = { item_id: 'item', source_location_id: 'location', quantity_base: 1 } satisfies components['schemas']['StockConsumeInput']
const transferStock = { item_id: 'item', source_location_id: 'source', target_location_id: 'target', quantity_base: 1 } satisfies components['schemas']['StockTransferInput']
const adjustStockToZero = { item_id: 'item', target_location_id: 'location', quantity_base: 0, reason: 'Explicit count' } satisfies components['schemas']['StockAdjustInput']
const emptyUnitPage = { data: [], next_cursor: null } satisfies components['schemas']['UnitList']
const emptyStockPage = { data: [], next_cursor: null } satisfies components['schemas']['StockList']
const deletedUnit = { ok: true, operation_id: 'event', revision: 4 } satisfies UnitDeletion
// @ts-expect-error The location decision is explicit even when it is null.
const unitWithoutLocation: components['schemas']['UnitCreate'] = { serial_number: '' }
// @ts-expect-error The immutable asset code is assigned by the authoritative service.
const callerUnitCode: components['schemas']['UnitCreate'] = { location_id: null, asset_code: 'caller' }
// @ts-expect-error New units cannot start with a terminal lifecycle state.
const terminalUnitCreate: components['schemas']['UnitCreate'] = { location_id: null, status: 'retired' }
// @ts-expect-error Only serial text is nullable; notes are exact nonnullable text.
const nullUnitNotes: components['schemas']['UnitWrite'] = { notes: null }
// @ts-expect-error Serial numbers are strings, never numeric coercions.
const numericUnitSerial: components['schemas']['UnitWrite'] = { serial_number: 123 }
// @ts-expect-error Owning Item identity is immutable on a unit metadata patch.
const reassignedUnit: components['schemas']['UnitWrite'] = { item_id: 'other' }
// @ts-expect-error Moving a unit is a separate original-unit-proof command.
const patchUnitLocation: components['schemas']['UnitWrite'] = { location_id: 'other' }
// @ts-expect-error Lifecycle transitions are not ordinary metadata patches.
const patchUnitStatus: components['schemas']['UnitWrite'] = { status: 'disposed' }
// @ts-expect-error A unit location is cleared only by explicit supported domain workflows, not a null move target.
const nullUnitMove: components['schemas']['UnitMove'] = { target_location_id: null }
// @ts-expect-error Quantity stock always uses integer-valued JSON numbers, not text.
const textHoldingQuantity: components['schemas']['StockReceiveInput'] = { item_id: 'item', target_location_id: 'target', quantity_base: '1000' }
// @ts-expect-error A transfer must identify both original locations.
const incompleteStockTransfer: components['schemas']['StockTransferInput'] = { item_id: 'item', target_location_id: 'target', quantity_base: 1 }
// @ts-expect-error Absolute adjustment requires an explicitly reviewed reason.
const reasonlessStockAdjustment: components['schemas']['StockAdjustInput'] = { item_id: 'item', target_location_id: 'target', quantity_base: 0 }
// @ts-expect-error Stock writes never install a caller-supplied measurement conversion.
const implicitStockConversion: components['schemas']['StockReceiveInput'] = { ...receiveStock, scale_factor: 1000 }
// @ts-expect-error A read balance must include its actual complete Item measurement tuple.
const incompleteStockMeasurement: components['schemas']['Stock'] = { item_id: 'item', location_id: 'location', revision: 1, quantity_base: 1 }
// @ts-expect-error Unit creation requires the original owning Item proof.
const unprovenHoldingCreation: UnitCreateHeaders = { ...authHeaders }
// @ts-expect-error Existing unit edits require their independent original Unit proof.
const unprovenHoldingEdit: UnitEditHeaders = { ...authHeaders }
// @ts-expect-error Stock mutation requires the original Item proof, not an implicit latest revision.
const unprovenStockMutation: StockMutationHeaders = { ...authHeaders }
// @ts-expect-error Same-origin protected unit writes require Origin in addition to the proof.
const originlessHoldingEdit: UnitEditHeaders = { 'If-Match': '"3"', 'X-CSRF-Token': 'csrf', 'Idempotency-Key': 'key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error A complete unit page must explicitly terminate or continue.
const incompleteUnitPage: components['schemas']['UnitList'] = { data: [] }
// @ts-expect-error A complete inventory page must explicitly terminate or continue.
const incompleteStockPage: components['schemas']['StockList'] = { data: [] }
// @ts-expect-error Unit deletion acknowledges its independent retained next revision.
const unversionedUnitDeletion: UnitDeletion = { ok: true, operation_id: 'event' }
void [reviewedHoldingItem, reviewedHoldingUnit, unitWithUnknownLocation, unitWithEmptySerial, unitSerialClear, unitNotesClear, unitMove, unitStatus,
  holdingUnit, holdingStock, receiveStock, consumeStock, transferStock, adjustStockToZero, emptyUnitPage, emptyStockPage, deletedUnit,
  unitWithoutLocation, callerUnitCode, terminalUnitCreate, nullUnitNotes, numericUnitSerial, reassignedUnit, patchUnitLocation, patchUnitStatus,
  nullUnitMove, textHoldingQuantity, incompleteStockTransfer, reasonlessStockAdjustment, implicitStockConversion, incompleteStockMeasurement,
  unprovenHoldingCreation, unprovenHoldingEdit, unprovenStockMutation, originlessHoldingEdit, incompleteUnitPage, incompleteStockPage, unversionedUnitDeletion]

type StockReadQuery = NonNullable<operations['get_inventory']['parameters']['query']>
const unfilteredStockRead = {} satisfies StockReadQuery
const filteredStockRead = { item_id: 'item', from: '2026-11-05', to: '2026-11-06', currency: 'TWD', category_id: 'category', location_id: 'location', scope: 'subtree', status: 'archived', merchant: 'Exact merchant', limit: 50, cursor: 'opaque' } satisfies StockReadQuery
// @ts-expect-error Inventory status describes Item metadata, not an individual Unit lifecycle.
const lifecycleStockRead: StockReadQuery = { status: 'stored' }
// @ts-expect-error Inventory has no catalog q parameter or implicit text-search mode.
const searchedStockRead: StockReadQuery = { q: 'uncontracted' }
// @ts-expect-error Query adapters must preserve a numeric page size.
const textStockReadSize: StockReadQuery = { limit: '50' }
// @ts-expect-error An omitted category filter is distinct from a null query field.
const nullStockReadCategory: StockReadQuery = { category_id: null }
const itemScopedStockRead = { item_id: 'item', limit: 1 } satisfies StockReadQuery
// @ts-expect-error Item selection is exact string identity, not an array of matches.
const multipleStockItemIDs: StockReadQuery = { item_id: ['item', 'other'] }
// @ts-expect-error An omitted Item criterion is distinct from a null query value.
const nullStockItemID: StockReadQuery = { item_id: null }
// @ts-expect-error The additive inventory criterion does not widen catalog queries.
const catalogItemIDFilter = fetchCatalogPage({ item_id: 'item' }, new AbortController().signal)
void [itemScopedStockRead, multipleStockItemIDs, nullStockItemID, catalogItemIDFilter]
void [unfilteredStockRead, filteredStockRead, lifecycleStockRead, searchedStockRead, textStockReadSize, nullStockReadCategory]

declare const holdingSDK: typeof import('../src/lib/api/holdings')
const independentUnitContext = { ...writeContext, itemID: currentItem.id, expectedRevision: holdingUnit.revision }
const reviewedQuantityItem = { ...currentItem, tracking_mode: 'quantity' as const, unit_code: 'kg', base_unit: 'g', scale_factor: 1000 }
const reviewedStockContext = { ...writeContext, item: reviewedQuantityItem }
const holdingSDKCalls = [
  holdingSDK.fetchUnit(holdingUnit.id, new AbortController().signal),
  holdingSDK.fetchItemUnitsPage(currentItem.id, { limit: 50 }, new AbortController().signal),
  holdingSDK.fetchStockPage(filteredStockRead, new AbortController().signal),
  holdingSDK.createUnit(currentItem.id, unitWithUnknownLocation, owningItemProof),
  holdingSDK.patchUnit(holdingUnit.id, unitSerialClear, independentUnitContext),
  holdingSDK.patchUnit(holdingUnit.id, unitNotesClear, independentUnitContext),
  holdingSDK.moveUnit(holdingUnit.id, unitMove, independentUnitContext),
  holdingSDK.setUnitStatus(holdingUnit.id, unitStatus, independentUnitContext),
  holdingSDK.setUnitStatus(holdingUnit.id, physicalUnitDisposal, independentUnitContext),
  holdingSDK.deleteUnit(holdingUnit.id, independentUnitContext),
  holdingSDK.receiveStock(receiveStock, reviewedStockContext),
  holdingSDK.consumeStock(consumeStock, reviewedStockContext),
  holdingSDK.transferStock(transferStock, reviewedStockContext),
  holdingSDK.adjustStock(adjustStockToZero, reviewedStockContext),
]
// @ts-expect-error Unit creation requires an explicit nullable location decision.
const implicitSDKLocation = holdingSDK.createUnit(currentItem.id, {}, owningItemProof)
// @ts-expect-error Unit creation requires the original Item proof.
const unprovenSDKUnit = holdingSDK.createUnit(currentItem.id, unitWithUnknownLocation, writeContext)
// @ts-expect-error Metadata editing requires at least one explicit field.
const emptySDKUnitPatch = holdingSDK.patchUnit(holdingUnit.id, {}, independentUnitContext)
// @ts-expect-error Independent Unit edits retain their original owner identity.
const ownerlessSDKUnitPatch = holdingSDK.patchUnit(holdingUnit.id, unitSerialClear, owningItemProof)
// @ts-expect-error Independent Unit deletion also retains the original owner.
const ownerlessSDKUnitDelete = holdingSDK.deleteUnit(holdingUnit.id, owningItemProof)
// @ts-expect-error Metadata editing cannot change physical location.
const implicitSDKUnitMove = holdingSDK.patchUnit(holdingUnit.id, { location_id: 'target' }, independentUnitContext)
// @ts-expect-error Physical disposal has an explicit discriminated action.
const ambiguousSDKDisposal = holdingSDK.setUnitStatus(holdingUnit.id, { status: 'disposed' }, independentUnitContext)
// @ts-expect-error Financial lifecycle input cannot bypass the asset command.
const financialSDKDisposal = holdingSDK.setUnitStatus(holdingUnit.id, { status: 'disposed', disposal_kind: 'sold', realized_proceeds_minor: 1 }, independentUnitContext)
// @ts-expect-error Stock proofs and measurement come from one whole reviewed Item, not a loose row revision.
const looseSDKStockProof = holdingSDK.receiveStock(receiveStock, owningItemProof)
// @ts-expect-error A Stock row cannot stand in for the reviewed authoritative Item.
const stockAsSDKItem = holdingSDK.receiveStock(receiveStock, { ...writeContext, item: holdingStock })
// @ts-expect-error An independently supplied proof cannot override the reviewed Item revision.
const overriddenSDKStockProof = holdingSDK.receiveStock(receiveStock, { ...reviewedStockContext, expectedRevision: 2 })
// @ts-expect-error Stock requests cannot silently install measurement conversion fields.
const convertedSDKStock = holdingSDK.receiveStock({ ...receiveStock, scale_factor: 1000 }, reviewedStockContext)
// @ts-expect-error Transfers require the actual source as well as destination.
const incompleteSDKTransfer = holdingSDK.transferStock(receiveStock, reviewedStockContext)
// @ts-expect-error An absolute physical adjustment always needs an explicit reason.
const reasonlessSDKAdjust = holdingSDK.adjustStock({ ...receiveStock, quantity_base: 0 }, reviewedStockContext)
// @ts-expect-error Unit pages accept only their collection paging fields.
const filteredSDKUnits = holdingSDK.fetchItemUnitsPage(currentItem.id, { status: 'stored' }, new AbortController().signal)
// @ts-expect-error Stock reads do not invent a text-search query.
const searchedSDKStock = holdingSDK.fetchStockPage({ q: 'uncontracted' }, new AbortController().signal)
void [holdingSDKCalls, implicitSDKLocation, unprovenSDKUnit, emptySDKUnitPatch, ownerlessSDKUnitPatch, ownerlessSDKUnitDelete, implicitSDKUnitMove,
  ambiguousSDKDisposal, financialSDKDisposal, looseSDKStockProof, stockAsSDKItem, overriddenSDKStockProof, convertedSDKStock, incompleteSDKTransfer,
  reasonlessSDKAdjust, filteredSDKUnits, searchedSDKStock]

type PurchaseCreateHeaders = operations['post_purchases']['parameters']['header']
type PurchaseEditHeaders = operations['patch_purchases_id']['parameters']['header']
type PurchaseLineHeaders = operations['post_purchases_id_lines']['parameters']['header']
type PurchaseQuery = NonNullable<operations['get_purchases']['parameters']['query']>
type PurchaseLineQuery = NonNullable<operations['get_purchases_id_lines']['parameters']['query']>
const purchaseHeaders = { ...authHeaders } satisfies PurchaseCreateHeaders
const purchaseProof = { ...authHeaders, 'If-Match': '"7"' } satisfies PurchaseEditHeaders & PurchaseLineHeaders
const purchaseHeader = { merchant_name: '  Merchant é  ', purchased_at: '2026-11-06T00:00:00Z', currency: 'JPY' } satisfies components['schemas']['PurchaseWrite']
const purchaseReceiptClear = { receipt_note: '' } satisfies components['schemas']['PurchasePatch']
const unallocatedPurchaseLine = { item_id: 'item', item_revision: 7, item_unit_id: null, quantity_base: 1500, paid_minor: 0 } satisfies components['schemas']['PurchaseLineWrite']
const allocatedPurchaseLine = { item_id: 'item', item_revision: 7, item_unit_id: 'unit', quantity_base: 1, paid_minor: 120000 } satisfies components['schemas']['PurchaseLineWrite']
const completePurchasePage = { data: [], next_cursor: null } satisfies components['schemas']['PurchaseList']
const completePurchaseLinePage = { data: [], next_cursor: null } satisfies components['schemas']['PurchaseLineList']
const filteredPurchases = { from: '2026-01-01', to: '2026-11-06', currency: 'JPY', category_id: 'category', location_id: 'location', scope: 'direct', status: 'active', merchant: 'Exact merchant', limit: 50, cursor: 'opaque' } satisfies PurchaseQuery
// @ts-expect-error Purchase writes require exact Origin independently of CSRF.
const originlessPurchase: PurchaseCreateHeaders = { 'X-CSRF-Token': 'csrf', 'Idempotency-Key': 'purchase-key', 'X-HomeBox-Client-Version': '0.1.0-dev' }
// @ts-expect-error Descriptive corrections require the original Purchase proof.
const unprovenPurchase: PurchaseEditHeaders = { ...authHeaders }
// @ts-expect-error Allocation creation uses the original Purchase proof too.
const unprovenPurchaseLine: PurchaseLineHeaders = { ...authHeaders }
// @ts-expect-error A new header has no unrelated Item or parent proof.
const unrelatedPurchaseProof: PurchaseCreateHeaders = { ...authHeaders, 'If-Match': '"3"' }
// @ts-expect-error Header metadata never counts the same paid amount a second time.
const headerPurchaseAmount: components['schemas']['PurchaseWrite'] = { ...purchaseHeader, paid_minor: 120000 }
// @ts-expect-error Nullable Unit allocation intent is explicit, not an omitted default.
const implicitPurchaseAllocation: components['schemas']['PurchaseLineWrite'] = { item_id: 'item', item_revision: 7, quantity_base: 1, paid_minor: 0 }
// @ts-expect-error One named physical Unit cannot silently represent a bulk price multiplier.
const bulkUnitPurchaseLine: components['schemas']['PurchaseLineWrite'] = { item_id: 'item', item_revision: 7, item_unit_id: 'unit', quantity_base: 2, paid_minor: 120000 }
// @ts-expect-error The original Purchase proof cannot substitute for the reviewed Item interpretation.
const unprovenPurchaseItem: components['schemas']['PurchaseLineWrite'] = { item_id: 'item', item_unit_id: null, quantity_base: 1500, paid_minor: 0 }
void unprovenPurchaseItem
// @ts-expect-error The complete minor-unit amount is a JSON number, not decimal display text.
const textPurchaseAmount: components['schemas']['PurchaseLineWrite'] = { ...unallocatedPurchaseLine, paid_minor: '1200' }
// @ts-expect-error Receipt text is nonnullable; empty clears it explicitly.
const nullPurchaseReceipt: components['schemas']['PurchasePatch'] = { receipt_note: null }
// @ts-expect-error Purchase pages explicitly continue or terminate.
const incompletePurchasePage: components['schemas']['PurchaseList'] = { data: [] }
// @ts-expect-error Allocation pages explicitly continue or terminate too.
const incompletePurchaseLinePage: components['schemas']['PurchaseLineList'] = { data: [] }
// @ts-expect-error The Purchase owner comes from the path, not the allocation body.
const reassignedPurchaseLine: components['schemas']['PurchaseLineWrite'] = { ...unallocatedPurchaseLine, purchase_id: 'other' }
// @ts-expect-error Purchase status filters describe current Item metadata, not Unit lifecycle.
const lifecyclePurchaseQuery: PurchaseQuery = { status: 'disposed' }
// @ts-expect-error The selected Purchase line collection accepts only paging fields.
const filteredPurchaseLines: PurchaseLineQuery = { currency: 'JPY' }
declare const purchaseSDK: typeof import('../src/lib/api/purchases')
declare const purchaseTransport: typeof import('../src/lib/api/client').apiClient
const reviewedPurchase = { id: 'purchase', revision: 7, ...purchaseHeader, receipt_note: 'Retained' } satisfies components['schemas']['Purchase']
const purchaseSDKContext = { ...writeContext, purchase: reviewedPurchase }
const purchaseSDKLineContext = { ...purchaseSDKContext, item: { ...reviewedQuantityItem, revision: 7 } }
const purchaseSDKCalls = [
  purchaseSDK.fetchPurchase(reviewedPurchase.id, new AbortController().signal),
  purchaseSDK.fetchPurchasesPage(filteredPurchases, new AbortController().signal),
  purchaseSDK.fetchItemPurchasesPage(currentItem.id, { limit: 50 }, new AbortController().signal),
  purchaseSDK.fetchPurchaseLinesPage(reviewedPurchase.id, { limit: 50 }, new AbortController().signal),
  purchaseSDK.createPurchase(purchaseHeader, writeContext),
  purchaseSDK.patchPurchase(reviewedPurchase.id, purchaseReceiptClear, purchaseSDKContext),
  purchaseSDK.createPurchaseLine(reviewedPurchase.id, unallocatedPurchaseLine, purchaseSDKLineContext),
  purchaseSDK.createPurchaseLine(reviewedPurchase.id, allocatedPurchaseLine, { ...purchaseSDKContext, item: { ...currentItem, revision: 7 } }),
]
const rawNullPurchaseLine = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: unallocatedPurchaseLine })
const rawNamedPurchaseLine = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: allocatedPurchaseLine })
// @ts-expect-error A new header has no unrelated aggregate proof.
const unrelatedSDKPurchaseProof = purchaseSDK.createPurchase(purchaseHeader, { ...writeContext, expectedRevision: 7 })
// @ts-expect-error Header metadata cannot double count a paid total.
const totalSDKPurchaseHeader = purchaseSDK.createPurchase({ ...purchaseHeader, paid_minor: 120000 }, writeContext)
// @ts-expect-error Corrections retain the whole original Purchase, not a loose proof.
const looseSDKPurchaseProof = purchaseSDK.patchPurchase(reviewedPurchase.id, purchaseReceiptClear, { ...writeContext, expectedRevision: 7 })
// @ts-expect-error An independently refreshed revision cannot override the reviewed header.
const overriddenSDKPurchaseProof = purchaseSDK.patchPurchase(reviewedPurchase.id, purchaseReceiptClear, { ...purchaseSDKContext, expectedRevision: 7 })
// @ts-expect-error A descriptive correction includes at least one actual field.
const emptySDKPurchasePatch = purchaseSDK.patchPurchase(reviewedPurchase.id, {}, purchaseSDKContext)
// @ts-expect-error Receipt text clears through empty, never null.
const nullSDKPurchaseReceipt = purchaseSDK.patchPurchase(reviewedPurchase.id, { receipt_note: null }, purchaseSDKContext)
// @ts-expect-error A Line also retains the whole original Item interpretation.
const ownerlessSDKPurchaseLine = purchaseSDK.createPurchaseLine(reviewedPurchase.id, unallocatedPurchaseLine, purchaseSDKContext)
// @ts-expect-error A Stock row is not an Item interpretation.
const stockAsSDKPurchaseItem = purchaseSDK.createPurchaseLine(reviewedPurchase.id, unallocatedPurchaseLine, { ...purchaseSDKContext, item: holdingStock })
// @ts-expect-error The body still requires original Item revision separately from Purchase proof.
const unprovenSDKPurchaseItem = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { item_id: 'item', item_unit_id: null, quantity_base: 1, paid_minor: 0 }, purchaseSDKLineContext)
// @ts-expect-error The nullable Unit decision cannot be omitted.
const implicitSDKPurchaseUnit = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { item_id: 'item', item_revision: 7, quantity_base: 1, paid_minor: 0 }, purchaseSDKLineContext)
// @ts-expect-error One named Unit has quantity exactly one, not a bulk multiplier.
const bulkSDKPurchaseUnit = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { ...allocatedPurchaseLine, quantity_base: 2 }, purchaseSDKLineContext)
// @ts-expect-error Money is the complete numeric minor-unit amount, not display text.
const textSDKPurchaseMoney = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { ...unallocatedPurchaseLine, paid_minor: '120000' }, purchaseSDKLineContext)
// @ts-expect-error A Line cannot override header currency.
const currencySDKPurchaseLine = purchaseSDK.createPurchaseLine(reviewedPurchase.id, { ...unallocatedPurchaseLine, currency: 'USD' }, purchaseSDKLineContext)
// @ts-expect-error Purchase collections have no catalog text query.
const searchedSDKPurchases = purchaseSDK.fetchPurchasesPage({ q: 'uncontracted' }, new AbortController().signal)
// @ts-expect-error Owned headers do not silently accept global filters.
const filteredSDKItemPurchases = purchaseSDK.fetchItemPurchasesPage(currentItem.id, { currency: 'JPY' }, new AbortController().signal)
// @ts-expect-error Owned Lines also accept only their original collection paging scope.
const filteredSDKPurchaseLines = purchaseSDK.fetchPurchaseLinesPage(reviewedPurchase.id, { status: 'active' }, new AbortController().signal)
// @ts-expect-error The shared transport must still require explicit nullable presence.
const implicitRawPurchaseUnit = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: { item_id: 'item', item_revision: 7, quantity_base: 1, paid_minor: 0 } })
// @ts-expect-error Null preservation must not widen the named-Unit oneOf constraint.
const bulkRawPurchaseUnit = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: { ...allocatedPurchaseLine, quantity_base: 2 } })
// @ts-expect-error A type-only write marker is never an actual wire value.
const markedRawPurchaseUnit = purchaseTransport.POST('/purchases/{id}/lines', { params: { path: { id: reviewedPurchase.id }, header: purchaseProof }, body: { ...unallocatedPurchaseLine, item_unit_id: { $write: null } } })
declare const purchaseModels: typeof import('../src/features/purchases/model')
declare const purchaseForms: typeof import('../src/features/purchases/form')
declare const purchaseCommands: typeof import('../src/features/purchases/commands')
const reviewedPurchaseDetail = { purchase: reviewedPurchase, lines: [] } satisfies import('../src/features/purchases/model').PurchaseDetail
const purchaseQuantityContext = { kind: 'quantity', purchase: reviewedPurchase, item: purchaseSDKLineContext.item } as const satisfies import('../src/features/purchases/model').PurchaseAllocationContext
const reviewedAllocation = purchaseForms.purchaseLineProposal({ ...purchaseForms.purchaseLineDefaults(), allocation: 'none', quantity_base: '1500', paid_minor: '0' }, purchaseQuantityContext)
const reviewedPurchaseCommands = [
  purchaseCommands.freezePurchaseCommand({ kind: 'create-purchase', body: purchaseHeader }),
  purchaseCommands.freezePurchaseCommand({ kind: 'patch-purchase', source: reviewedPurchaseDetail, body: purchaseReceiptClear }),
  purchaseCommands.freezePurchaseCommand({ kind: 'create-line', ...reviewedAllocation }),
]
const purchaseReviewReads = [purchaseModels.readPurchaseDetail(reviewedPurchase.id, new AbortController().signal),
  purchaseModels.readItemPurchases(currentItem.id, new AbortController().signal), purchaseModels.readPurchaseAllocation(reviewedPurchase.id, currentItem.id, new AbortController().signal)]
// @ts-expect-error Header correction carries its complete original source context.
const unprovenReviewedPurchase = purchaseCommands.freezePurchaseCommand({ kind: 'patch-purchase', body: purchaseReceiptClear })
// @ts-expect-error Allocation carries a real original Item interpretation.
const ownerlessReviewedAllocation = purchaseCommands.freezePurchaseCommand({ kind: 'create-line', purchase: reviewedPurchase, unit: null, body: unallocatedPurchaseLine })
// @ts-expect-error Even no named Unit must be an explicit nullable review decision.
const implicitReviewedUnit = purchaseCommands.freezePurchaseCommand({ kind: 'create-line', purchase: reviewedPurchase, item: currentItem, body: unallocatedPurchaseLine })
// @ts-expect-error Posted purchase deletion is not one of the reviewed commands.
const deletedReviewedPurchase = purchaseCommands.freezePurchaseCommand({ kind: 'delete-purchase', source: reviewedPurchaseDetail })
// @ts-expect-error Individual selection requires the complete Unit collection, not an invented unqueried set.
const incompletePurchaseContext: import('../src/features/purchases/model').PurchaseAllocationContext = { kind: 'individual', purchase: reviewedPurchase, item: currentItem }
// @ts-expect-error Quantity selection does not pretend to query the Unit domain.
const inventedPurchaseUnits: import('../src/features/purchases/model').PurchaseAllocationContext = { kind: 'quantity', purchase: reviewedPurchase, item: currentItem, units: [] }
// @ts-expect-error Original reviewed command keys cannot be rebased or reassigned.
reviewedPurchaseCommands[0]!.key = 'changed-command-key'
void [reviewedPurchaseCommands, purchaseReviewReads, unprovenReviewedPurchase, ownerlessReviewedAllocation, implicitReviewedUnit, deletedReviewedPurchase, incompletePurchaseContext, inventedPurchaseUnits]

void [purchaseSDKCalls, rawNullPurchaseLine, rawNamedPurchaseLine, unrelatedSDKPurchaseProof, totalSDKPurchaseHeader, looseSDKPurchaseProof,
  overriddenSDKPurchaseProof, emptySDKPurchasePatch, nullSDKPurchaseReceipt, ownerlessSDKPurchaseLine, stockAsSDKPurchaseItem, unprovenSDKPurchaseItem,
  implicitSDKPurchaseUnit, bulkSDKPurchaseUnit, textSDKPurchaseMoney, currencySDKPurchaseLine, searchedSDKPurchases, filteredSDKItemPurchases,
  filteredSDKPurchaseLines, implicitRawPurchaseUnit, bulkRawPurchaseUnit, markedRawPurchaseUnit]

// Asset wire shapes remain distinct from runtime adapter validation and UI.
const assetFields = { cost_basis: 'manual' as const, currency: 'JPY', manual_acquisition_minor: 0,
  in_service_at: null, retired_at: null, estimated_residual_minor: null, usage_unit: 'times' as const }
const manualAssetInput = { ...assetFields, item_revision: 11, asset_revision: null } satisfies components['schemas']['AssetWrite']
const purchaseAssetInput = { ...manualAssetInput, cost_basis: 'purchase', manual_acquisition_minor: null } satisfies components['schemas']['AssetWrite']
const retainedAsset = { ...assetFields, item_unit_id: 'unit', revision: 7 } satisfies components['schemas']['Asset']
const assetUseInput = { event_type: 'use', delta: 1, usage_unit: 'times', occurred_at: '2026-11-07T00:00:00Z' } satisfies components['schemas']['UsageWrite']
const assetCorrectionInput = { ...assetUseInput, event_type: 'correction', delta: -1, correction_of: 'use' } satisfies components['schemas']['UsageWrite']
const assetCostInput = { kind: 'repair', direction: 'expense', amount_minor: 5000, currency: 'JPY', occurred_at: '2026-11-07T00:00:00Z' } satisfies components['schemas']['CostWrite']
const assetAdjustmentInput = { ...assetCostInput, kind: 'adjustment', direction: 'credit', correction_of: 'cost' } satisfies components['schemas']['CostWrite']
const assetRetireInput = { retired_at: '2026-11-07T00:00:00Z', unit_revision: 13 } satisfies components['schemas']['UnitRetireInput']
const assetDisposalInput = { kind: 'sold', disposed_at: '2026-11-07T00:00:00Z', currency: 'JPY', realized_proceeds_minor: 0, unit_revision: 13 } satisfies components['schemas']['DisposalWrite']
const assetNonSaleInput = { ...assetDisposalInput, kind: 'donated', realized_proceeds_minor: 0 } satisfies components['schemas']['DisposalWrite']
const assetHeaders = { ...authHeaders, 'If-Match': '"7"' } satisfies operations['put_units_id_asset']['parameters']['header'] & operations['post_units_id_usage']['parameters']['header']
const assetPages = [
  { data: [], next_cursor: null } satisfies components['schemas']['AssetList'],
  { data: [], next_cursor: null } satisfies components['schemas']['UsageList'],
  { data: [], next_cursor: null } satisfies components['schemas']['CostList'],
]
const assetTransportCalls = [
  purchaseTransport.GET('/assets', { params: { query: { currency: 'JPY', status: 'active', merchant: 'Exact\nmerchant', limit: 50 } } }),
  purchaseTransport.GET('/units/{id}/asset', { params: { path: { id: 'unit' } } }),
  purchaseTransport.GET('/units/{id}/usage', { params: { path: { id: 'unit' }, query: { limit: 50 } } }),
  purchaseTransport.GET('/units/{id}/costs', { params: { path: { id: 'unit' }, query: { limit: 50 } } }),
  purchaseTransport.GET('/units/{id}/disposal', { params: { path: { id: 'unit' } } }),
  purchaseTransport.GET('/units/{id}/cost-summary', { params: { path: { id: 'unit' }, query: { as_of: '2026-11-07T00:00:00Z' } } }),
  purchaseTransport.PUT('/units/{id}/asset', { params: { path: { id: 'unit' }, header: assetHeaders }, body: manualAssetInput }),
  purchaseTransport.PUT('/units/{id}/asset', { params: { path: { id: 'unit' }, header: assetHeaders }, body: purchaseAssetInput }),
  purchaseTransport.POST('/units/{id}/usage', { params: { path: { id: 'unit' }, header: assetHeaders }, body: assetUseInput }),
  purchaseTransport.POST('/units/{id}/usage', { params: { path: { id: 'unit' }, header: assetHeaders }, body: assetCorrectionInput }),
  purchaseTransport.POST('/units/{id}/costs', { params: { path: { id: 'unit' }, header: assetHeaders }, body: assetCostInput }),
  purchaseTransport.POST('/units/{id}/costs', { params: { path: { id: 'unit' }, header: assetHeaders }, body: assetAdjustmentInput }),
  purchaseTransport.POST('/units/{id}/retire', { params: { path: { id: 'unit' }, header: assetHeaders }, body: assetRetireInput }),
  purchaseTransport.POST('/units/{id}/disposal', { params: { path: { id: 'unit' }, header: assetHeaders }, body: assetDisposalInput }),
]
// @ts-expect-error Opt-in/replacement intent is explicit, not an omitted Asset proof.
const implicitAssetIntent: components['schemas']['AssetWrite'] = { ...assetFields, item_revision: 11 }
// @ts-expect-error The Unit If-Match does not freeze the owning Item's acquisition interpretation.
const unprovenAssetItem: components['schemas']['AssetWrite'] = { ...assetFields, asset_revision: null }
// @ts-expect-error PUT is a complete nullable decision, not a partial profile patch.
const partialAssetInput: components['schemas']['AssetWrite'] = { cost_basis: 'manual', currency: 'JPY', manual_acquisition_minor: 0, usage_unit: 'times', item_revision: 11, asset_revision: null }
// @ts-expect-error Manual fallback cannot be null even though the retained response can represent legacy source.
const nullManualAsset: components['schemas']['AssetWrite'] = { ...manualAssetInput, manual_acquisition_minor: null }
// @ts-expect-error Purchase basis cannot also contain manual acquisition, including zero.
const doubleAssetAcquisition: components['schemas']['AssetWrite'] = { ...purchaseAssetInput, manual_acquisition_minor: 0 }
// @ts-expect-error Profile ownership comes from the actual Unit path.
const reassignedAssetInput: components['schemas']['AssetWrite'] = { ...manualAssetInput, item_unit_id: 'other' }
// @ts-expect-error A correction must identify the actual earlier event.
const unreferencedAssetUsage: components['schemas']['UsageWrite'] = { ...assetUseInput, event_type: 'correction' }
// @ts-expect-error A new use is not a correction of an existing event.
const referencedAssetUse: components['schemas']['UsageWrite'] = { ...assetUseInput, correction_of: 'use' }
// @ts-expect-error An adjustment cannot silently become an unreferenced expense/credit.
const unreferencedAssetCost: components['schemas']['CostWrite'] = { ...assetCostInput, kind: 'adjustment' }
// @ts-expect-error A real refund is distinct from an adjustment of an earlier event.
const referencedAssetRefund: components['schemas']['CostWrite'] = { ...assetCostInput, kind: 'refund', correction_of: 'cost' }
// @ts-expect-error Retirement also binds the original physical Unit independently of Asset If-Match.
const unprovenAssetRetire: components['schemas']['UnitRetireInput'] = { retired_at: '2026-11-07T00:00:00Z' }
// @ts-expect-error Disposal proceeds are explicit even when zero.
const implicitAssetProceeds: components['schemas']['DisposalWrite'] = { kind: 'sold', disposed_at: '2026-11-07T00:00:00Z', currency: 'JPY', unit_revision: 13 }
// @ts-expect-error Non-sale decisions cannot disguise proceeds under a donated action.
const nonSaleAssetProceeds: components['schemas']['DisposalWrite'] = { ...assetDisposalInput, kind: 'donated', realized_proceeds_minor: 1 }
// @ts-expect-error Unit proof is required and nonnullable for a physical disposal.
const nullAssetUnitProof: components['schemas']['DisposalWrite'] = { ...assetDisposalInput, unit_revision: null }
// @ts-expect-error Usage replies require the whole immutable event, not only its identity.
const partialAssetUsage: components['schemas']['Usage'] = { id: 'use', item_unit_id: 'unit', revision: 1 }
// @ts-expect-error Cost replies require the whole immutable event too.
const partialAssetCost: components['schemas']['Cost'] = { id: 'cost', item_unit_id: 'unit', revision: 1 }
// @ts-expect-error Disposal replies retain the actual decision and proceeds.
const partialAssetDisposal: components['schemas']['Disposal'] = { item_unit_id: 'unit', revision: 1 }
// @ts-expect-error Asset pages explicitly terminate or continue.
const partialAssetPage: components['schemas']['AssetList'] = { data: [] }
// @ts-expect-error Usage pages explicitly terminate or continue.
const partialAssetUsagePage: components['schemas']['UsageList'] = { data: [] }
// @ts-expect-error Cost pages explicitly terminate or continue.
const partialAssetCostPage: components['schemas']['CostList'] = { data: [] }
// @ts-expect-error Every financial writer still requires its original If-Match.
const unprovenAssetHeaders: operations['post_units_id_costs']['parameters']['header'] = { ...authHeaders }
// @ts-expect-error Actual client requests must preserve the required nullable Asset proof.
const implicitRawAssetIntent = purchaseTransport.PUT('/units/{id}/asset', { params: { path: { id: 'unit' }, header: assetHeaders }, body: { ...assetFields, item_revision: 11 } })
// @ts-expect-error The public write marker is type-only, never a wire object.
const markedRawAssetIntent = purchaseTransport.PUT('/units/{id}/asset', { params: { path: { id: 'unit' }, header: assetHeaders }, body: { ...manualAssetInput, asset_revision: { $write: null } } })
// @ts-expect-error Direct transport cannot erase correction reference requirements.
const unreferencedRawAssetCost = purchaseTransport.POST('/units/{id}/costs', { params: { path: { id: 'unit' }, header: assetHeaders }, body: { ...assetCostInput, kind: 'adjustment' } })
// @ts-expect-error Asset status selects Item metadata, not physical lifecycle.
const lifecycleAssetFilter: NonNullable<operations['get_assets']['parameters']['query']> = { status: 'disposed' }
void [manualAssetInput, purchaseAssetInput, retainedAsset, assetUseInput, assetCorrectionInput, assetCostInput, assetAdjustmentInput,
  assetRetireInput, assetDisposalInput, assetNonSaleInput, assetPages, assetTransportCalls, implicitAssetIntent, unprovenAssetItem, partialAssetInput,
  nullManualAsset, doubleAssetAcquisition, reassignedAssetInput, unreferencedAssetUsage, referencedAssetUse, unreferencedAssetCost, referencedAssetRefund,
  unprovenAssetRetire, implicitAssetProceeds, nonSaleAssetProceeds, nullAssetUnitProof, partialAssetUsage, partialAssetCost, partialAssetDisposal,
  partialAssetPage, partialAssetUsagePage, partialAssetCostPage, unprovenAssetHeaders, implicitRawAssetIntent, markedRawAssetIntent, unreferencedRawAssetCost, lifecycleAssetFilter]

declare const assetCommands: typeof import('../src/features/assets/commands')
declare const assetMutation: ReturnType<typeof import('../src/features/assets/use-asset-mutation').useAssetMutation>
const reviewedAssetCommands = [
  assetCommands.freezeAssetCommand({ kind: 'put-profile', source: reviewedAssetProfileSource, body: manualAssetInput }),
  assetCommands.freezeAssetCommand({ kind: 'record-usage', source: reviewedAssetUsageSource, body: assetCorrectionInput }),
  assetCommands.freezeAssetCommand({ kind: 'record-cost', source: reviewedAssetCostsSource, body: assetAdjustmentInput }),
  assetCommands.freezeAssetCommand({ kind: 'retire-asset', source: reviewedAssetLifecycleSource, body: assetRetireInput }),
  assetCommands.freezeAssetCommand({ kind: 'dispose-asset', source: reviewedAssetDetailSource, body: assetDisposalInput }),
]
const frozenAssetUsage = assetCommands.freezeAssetCommand({ kind: 'record-usage', source: reviewedAssetUsageSource, body: assetUseInput })
const sentAssetCommand = assetCommands.sendAssetCommand(frozenAssetUsage, 'fresh-csrf', new AbortController().signal)
assetMutation.review({ kind: 'record-cost', source: reviewedAssetCostsSource, body: assetCostInput })
// @ts-expect-error Reviewed commands require a complete original source, not loose proofs.
const looseAssetCommand = assetCommands.freezeAssetCommand({ kind: 'put-profile', source: { expectedRevision: 7 }, body: manualAssetInput })
// @ts-expect-error Usage commands retain their selected complete history, not a Cost source.
const wrongAssetCommandHistory = assetCommands.freezeAssetCommand({ kind: 'record-usage', source: reviewedAssetCostsSource, body: assetUseInput })
// @ts-expect-error Lifecycle commands retain explicit nullable Disposal context too.
const partialAssetCommandLifecycle = assetCommands.freezeAssetCommand({ kind: 'dispose-asset', source: { item: currentItem, unit: holdingUnit, asset: retainedAsset }, body: assetDisposalInput })
// @ts-expect-error Calculation metadata is not an Asset command source or proof.
const summaryAssetCommand = assetCommands.freezeAssetCommand({ kind: 'record-cost', source: reviewedAssetDetailSource.summary, body: assetCostInput })
// @ts-expect-error Raw monetary strings must pass the exact form proposal boundary first.
const rawAssetCommand = assetCommands.freezeAssetCommand({ kind: 'record-cost', source: reviewedAssetCostsSource, body: { ...assetCostInput, amount_minor: '1' } })
// @ts-expect-error Keys belong to the frozen command, not supplied draft input.
const suppliedAssetCommandKey = assetCommands.freezeAssetCommand({ kind: 'record-usage', source: reviewedAssetUsageSource, body: assetUseInput, key: 'replacement' })
// @ts-expect-error Original keys remain read-only after review.
frozenAssetUsage.key = 'replacement'
// @ts-expect-error Confirmation uses the retained command; it does not adopt another body.
assetMutation.confirm({ body: assetUseInput })
void [reviewedAssetCommands, sentAssetCommand, looseAssetCommand, wrongAssetCommandHistory, partialAssetCommandLifecycle, summaryAssetCommand, rawAssetCommand, suppliedAssetCommandKey]

declare const assetEditor: typeof import('../src/features/assets/editor-model')
declare const assetNavigation: typeof import('../src/lib/asset-navigation')
const assetEditors = [
  { kind: 'put-profile', source: reviewedAssetProfileSource },
  { kind: 'record-usage', source: reviewedAssetUsageSource },
  { kind: 'record-cost', source: reviewedAssetCostsSource },
  { kind: 'retire-asset', source: reviewedAssetLifecycleSource },
  { kind: 'dispose-asset', source: reviewedAssetLifecycleSource },
] as const satisfies readonly import('../src/features/assets/editor-model').AssetEditorData[]
const assetUIValues = assetEditor.assetEditorDefaults(assetEditors[0])
const assetUIProposal: import('../src/features/assets/commands').AssetCommandInput = assetEditor.assetEditorProposal(assetEditors[0], assetUIValues)
const assetUIRoutes = [
  assetNavigation.assetRoute('new', 'usage/new'),
  assetNavigation.assetListRoute({ currency: 'JPY', merchant: 'Exact\nmerchant', limit: 50 }),
  assetNavigation.assetSummaryRoute('unit', { as_of: '2026-11-07T00:00:00.123456789Z' }),
]
// @ts-expect-error A calculation is display context, not an editor's original financial source.
const summaryAssetEditor: import('../src/features/assets/editor-model').AssetEditorData = { kind: 'record-cost', source: reviewedAssetDetailSource.summary }
// @ts-expect-error A usage editor must retain its complete selected Usage collection, not Costs.
const wrongHistoryAssetEditor: import('../src/features/assets/editor-model').AssetEditorData = { kind: 'record-usage', source: reviewedAssetCostsSource }
// @ts-expect-error Lifecycle review requires explicit nullable Disposal context.
const implicitDisposalAssetEditor: import('../src/features/assets/editor-model').AssetEditorData = { kind: 'dispose-asset', source: { item: currentItem, unit: holdingUnit, asset: retainedAsset } }
// @ts-expect-error Editor values retain raw text until the exact proposal boundary.
const numericAssetDraft = assetEditor.assetEditorProposal(assetEditors[2], { amount_minor: 1 })
// @ts-expect-error Browser route modes are implemented UI paths, not API endpoint names.
const rawAPIAssetRoute = assetNavigation.assetRoute('unit', 'cost-summary')
// @ts-expect-error Amounts and commands never belong to global read filters or login returns.
const monetaryAssetRoute = assetNavigation.assetListRoute({ currency: 'JPY', paid_minor: 1 })
// @ts-expect-error Summary read scope cannot carry a write proof.
const provenAssetUIRoute = assetNavigation.assetSummaryRoute('unit', { as_of: '2026-11-07T00:00:00Z', expectedRevision: 7 })
void [assetUIProposal, assetUIRoutes, summaryAssetEditor, wrongHistoryAssetEditor, implicitDisposalAssetEditor,
  numericAssetDraft, rawAPIAssetRoute, monetaryAssetRoute, provenAssetUIRoute]

import * as assetForms from '../src/features/assets/form'
declare const reviewedAssetProfileSource: import('../src/features/assets/model').AssetProfileSource
declare const reviewedAssetCostsSource: import('../src/features/assets/model').AssetCostsSource
const rawAssetProfile = assetForms.assetProfileDefaults(reviewedAssetProfileSource)
const rawAssetUsage = assetForms.assetUsageDefaults(reviewedAssetUsageSource)
const rawAssetCost = assetForms.assetCostDefaults(reviewedAssetCostsSource)
const rawAssetRetire = assetForms.assetRetireDefaults(reviewedAssetLifecycleSource)
const rawAssetDisposal = assetForms.assetDisposalDefaults(reviewedAssetLifecycleSource)
const assetFormProposals = [assetForms.assetProfileBody(rawAssetProfile, reviewedAssetProfileSource), assetForms.assetUsageBody(rawAssetUsage, reviewedAssetUsageSource),
  assetForms.assetCostBody(rawAssetCost, reviewedAssetCostsSource), assetForms.assetRetireBody(rawAssetRetire, reviewedAssetLifecycleSource), assetForms.assetDisposalBody(rawAssetDisposal, reviewedAssetLifecycleSource)]
// @ts-expect-error Form proposals retain complete source, not a loose numeric proof.
const looseAssetFormSource = assetForms.assetProfileBody(rawAssetProfile, { expectedRevision: 7 })
// @ts-expect-error Raw minor-unit money remains string input even when zero is intended.
const numericRawAssetCost = assetForms.assetCostBody({ ...rawAssetCost, amount_minor: 0 }, reviewedAssetCostsSource)
// @ts-expect-error Selected correction sources cannot be replaced by another history kind.
const wrongAssetHistoryForm = assetForms.assetUsageBody(rawAssetUsage, reviewedAssetCostsSource)
// @ts-expect-error Disposal retains original physical/financial and nullable Disposal source context.
const partialAssetDisposalForm = assetForms.assetDisposalBody(rawAssetDisposal, { asset: retainedAsset, unit: holdingUnit })
void [assetFormProposals, looseAssetFormSource, numericRawAssetCost, wrongAssetHistoryForm, partialAssetDisposalForm]

import * as assetModels from '../src/features/assets/model'
declare const reviewedAssetUsageSource: assetModels.AssetUsageSource
declare const reviewedAssetLifecycleSource: assetModels.AssetLifecycleSource
declare const reviewedAssetDetailSource: assetModels.AssetDetail
const assetSourceReads = [assetModels.readAssetProfile(holdingUnit.id, new AbortController().signal),
  assetModels.readAssetUsage(holdingUnit.id, new AbortController().signal), assetModels.readAssetCosts(holdingUnit.id, new AbortController().signal),
  assetModels.readAssetLifecycle(holdingUnit.id, new AbortController().signal), assetModels.readAssetDetail(holdingUnit.id, {}, new AbortController().signal)]
const sourcedSDKAssetUsage = assetSDK.recordAssetUsage(holdingUnit.id, assetUseInput, { ...writeContext, asset: reviewedAssetUsageSource.asset })
const sourcedSDKAssetDisposal = assetSDK.disposeAsset(holdingUnit.id, assetDisposalInput, { ...writeContext, ...reviewedAssetLifecycleSource })
// @ts-expect-error A selected Usage collection does not invent an unqueried Cost set.
const inventedAssetSourceCosts = reviewedAssetUsageSource.costs
// @ts-expect-error Calculation metadata is not an original Asset proof/context.
const summaryAsAssetProof = assetSDK.recordAssetUsage(holdingUnit.id, assetUseInput, { ...writeContext, asset: reviewedAssetDetailSource.summary })
// @ts-expect-error Lifecycle source keeps explicit nullable Disposal presence.
const implicitAssetLifecycleSource: assetModels.AssetLifecycleSource = { item: currentItem, unit: holdingUnit, asset: retainedAsset }
// @ts-expect-error Complete retained event collections remain read-only review data.
reviewedAssetUsageSource.usage.push(partialAssetUsage)
void [assetSourceReads, sourcedSDKAssetUsage, sourcedSDKAssetDisposal, inventedAssetSourceCosts, summaryAsAssetProof, implicitAssetLifecycleSource]

import * as assetSDK from '../src/lib/api/assets'
const assetSDKContext = { ...writeContext, asset: retainedAsset }
const assetSDKProfileContext = { ...assetSDKContext, unit: holdingUnit, item: currentItem }
const assetSDKLifecycleContext = { ...assetSDKContext, unit: holdingUnit }
const assetSDKDisposalContext = { ...assetSDKLifecycleContext, disposal: null }
const assetSDKCalls = [
  assetSDK.fetchAsset(holdingUnit.id, new AbortController().signal),
  assetSDK.fetchAssetsPage({ currency: 'JPY', merchant: 'Exact\nmerchant', status: 'active', limit: 50 }, new AbortController().signal),
  assetSDK.fetchAssetUsagePage(holdingUnit.id, { limit: 50 }, new AbortController().signal),
  assetSDK.fetchAssetCostsPage(holdingUnit.id, { cursor: 'signed' }, new AbortController().signal),
  assetSDK.fetchAssetDisposal(holdingUnit.id, new AbortController().signal),
  assetSDK.fetchAssetCostSummary(holdingUnit.id, { as_of: '2026-11-07T00:00:00Z' }, new AbortController().signal),
  assetSDK.putAssetProfile(holdingUnit.id, manualAssetInput, { ...assetSDKProfileContext, asset: null }),
  assetSDK.putAssetProfile(holdingUnit.id, { ...manualAssetInput, asset_revision: retainedAsset.revision }, assetSDKProfileContext),
  assetSDK.putAssetProfile(holdingUnit.id, purchaseAssetInput, { ...assetSDKProfileContext, asset: null }),
  assetSDK.recordAssetUsage(holdingUnit.id, assetUseInput, assetSDKContext),
  assetSDK.recordAssetUsage(holdingUnit.id, assetCorrectionInput, assetSDKContext),
  assetSDK.recordAssetCost(holdingUnit.id, assetCostInput, assetSDKContext),
  assetSDK.recordAssetCost(holdingUnit.id, assetAdjustmentInput, assetSDKContext),
  assetSDK.retireAsset(holdingUnit.id, assetRetireInput, assetSDKLifecycleContext),
  assetSDK.disposeAsset(holdingUnit.id, assetDisposalInput, assetSDKDisposalContext),
  assetSDK.disposeAsset(holdingUnit.id, assetNonSaleInput, assetSDKDisposalContext),
]
// @ts-expect-error Profile opt-in retains the whole original Unit/Item and explicit nullable Asset.
const looseSDKAssetProfile = assetSDK.putAssetProfile(holdingUnit.id, manualAssetInput, { ...writeContext, expectedRevision: 13 })
// @ts-expect-error Reviewed absence is explicit, never an omitted context field.
const implicitSDKAssetPresence = assetSDK.putAssetProfile(holdingUnit.id, manualAssetInput, { ...writeContext, unit: holdingUnit, item: currentItem })
// @ts-expect-error An owning Item proof is not inferred from the Unit or Asset.
const itemlessSDKAssetProfile = assetSDK.putAssetProfile(holdingUnit.id, manualAssetInput, { ...writeContext, unit: holdingUnit, asset: null })
// @ts-expect-error A loose proof cannot override any whole original profile scope.
const overriddenSDKAssetProfile = assetSDK.putAssetProfile(holdingUnit.id, manualAssetInput, { ...assetSDKProfileContext, expectedRevision: 13 })
// @ts-expect-error A partial profile is not a complete financial decision.
const partialSDKAssetProfile = assetSDK.putAssetProfile(holdingUnit.id, { cost_basis: 'manual' }, assetSDKProfileContext)
// @ts-expect-error Nullable profile proof cannot be a real request marker object.
const markedSDKAssetProfile = assetSDK.putAssetProfile(holdingUnit.id, { ...manualAssetInput, asset_revision: { $write: null } }, assetSDKProfileContext)
// @ts-expect-error Usage retains the whole original Asset, not a loose counter.
const looseSDKAssetUsage = assetSDK.recordAssetUsage(holdingUnit.id, assetUseInput, { ...writeContext, expectedRevision: 7 })
// @ts-expect-error Cost cannot override the original Asset proof.
const overriddenSDKAssetCost = assetSDK.recordAssetCost(holdingUnit.id, assetCostInput, { ...assetSDKContext, expectedRevision: 7 })
// @ts-expect-error Corrections still need a real reference through the adapter signature.
const unreferencedSDKAssetUsage = assetSDK.recordAssetUsage(holdingUnit.id, { ...assetUseInput, event_type: 'correction' }, assetSDKContext)
// @ts-expect-error Ordinary refunds do not claim a correction target.
const referencedSDKAssetRefund = assetSDK.recordAssetCost(holdingUnit.id, { ...assetCostInput, kind: 'refund', correction_of: 'cost' }, assetSDKContext)
// @ts-expect-error Retirement retains the independent original Unit context as well as Asset.
const unitlessSDKAssetRetire = assetSDK.retireAsset(holdingUnit.id, assetRetireInput, assetSDKContext)
// @ts-expect-error Retained Disposal presence is explicit review context, not another invented wire proof.
const implicitSDKAssetDisposal = assetSDK.disposeAsset(holdingUnit.id, assetDisposalInput, assetSDKLifecycleContext)
// @ts-expect-error Zero proceeds cannot be omitted.
const implicitSDKAssetProceeds = assetSDK.disposeAsset(holdingUnit.id, { kind: 'sold', disposed_at: '2026-11-07T00:00:00Z', currency: 'JPY', unit_revision: 13 }, assetSDKDisposalContext)
// @ts-expect-error Non-sale money cannot bypass the generated discriminant through an SDK call.
const nonSaleSDKAssetProceeds = assetSDK.disposeAsset(holdingUnit.id, { ...assetDisposalInput, kind: 'other', realized_proceeds_minor: 1 }, assetSDKDisposalContext)
// @ts-expect-error Asset pages have no catalog q or synthetic current-Unit status filter.
const searchedSDKAssets = assetSDK.fetchAssetsPage({ q: 'private' }, new AbortController().signal)
// @ts-expect-error Owned immutable history has no currency filtering or client-side repricing.
const filteredSDKAssetUsage = assetSDK.fetchAssetUsagePage(holdingUnit.id, { currency: 'JPY' }, new AbortController().signal)
// @ts-expect-error The calculation accepts only its optional as_of, never an Asset proof.
const provenSDKAssetSummary = assetSDK.fetchAssetCostSummary(holdingUnit.id, { expectedRevision: 7 }, new AbortController().signal)
void [assetSDKCalls, looseSDKAssetProfile, implicitSDKAssetPresence, itemlessSDKAssetProfile, overriddenSDKAssetProfile, partialSDKAssetProfile,
  markedSDKAssetProfile, looseSDKAssetUsage, overriddenSDKAssetCost, unreferencedSDKAssetUsage, referencedSDKAssetRefund, unitlessSDKAssetRetire,
  implicitSDKAssetDisposal, implicitSDKAssetProceeds, nonSaleSDKAssetProceeds, searchedSDKAssets, filteredSDKAssetUsage, provenSDKAssetSummary]

void [purchaseHeaders, purchaseProof, purchaseHeader, purchaseReceiptClear, unallocatedPurchaseLine, allocatedPurchaseLine, completePurchasePage, completePurchaseLinePage,
  filteredPurchases, originlessPurchase, unprovenPurchase, unprovenPurchaseLine, unrelatedPurchaseProof, headerPurchaseAmount, implicitPurchaseAllocation,
  bulkUnitPurchaseLine, textPurchaseAmount, nullPurchaseReceipt, incompletePurchasePage, incompletePurchaseLinePage, reassignedPurchaseLine, lifecyclePurchaseQuery, filteredPurchaseLines]

void [ownedSDKCalls, unprovenOwnedBarcode, unprovenOwnedAlias, unprovenOwnedAttribute, ownerlessBarcodePatch, ownerlessAliasDelete, emptyOwnedAttributePatch,
  missingOwnedAttributeValue, normalizedOwnedInput, reassignedOwnedInput, unprovenTagSet, nullTagSet, filteredOwnedRead]

void [ownedCreateHeaders, barcodeBody, aliasBody, aliasResource, attributeBody, attributePartial, tagMembershipPage, clearMembership, tagReplacementResult,
  missingOwnedOrigin, numericBarcode, callerNormalizedBarcode, reassignedAlias, incompleteAlias, incompleteAttribute, nullAttribute, incompleteMembershipPage, partialMembershipResult,
  createMinimalCatalog, createDefaultSubset, createMeasuredCatalog, createClassifiedCatalog, patchCatalogNull, archiveCatalog, deleteCatalog, readFilteredCatalog,
  incompleteCatalogUnit, incompleteCatalogBase, incompleteCatalogScale, unrelatedCatalogProof, unprovenCatalogPatch, unprovenCatalogArchive, unprovenCatalogDelete,
  implicitCatalogQuantity, invalidCatalogStatus,
  itemHeaders, itemProof, measuredItem, clearItem, restoreItem, emptyItemPage, currentItem,
  itemWithoutOrigin, itemWithoutProof, wrappedItem, nullItemScale, deletedItem, implicitItemStock, incompleteItemPage,
  rootCategory, childCategory, createNamedTag, patchCategoryParent, unprovenCategoryChild, unrelatedCategoryProof, unrelatedTagProof, unprovenCategoryMove,
  rootCategoryHeaders, childCategoryHeaders, categoryCreate, categoryMove, categoryRename, category, tag,
  missingCategoryName, missingCategoryParent, categoryParentWrapper, missingCategoryOrigin, nullTag,
  createRoot, createChild, unprovenChild, unrelatedRootProof,
  invalidDelete, missingRevision, invalidQuantity, authHeaders, noOrigin, noCSRF, rawCredentialAdmin, hashLogin,
  childLocationHeaders, rootLocationHeaders, locationWithoutOrigin, rootLocation, minimalLocation, minimalItem, rootMove, parentWrapper, textSortOrder, nullKind]

// These transport fixtures check contracts independently of the browser SDK.
const lineVoidDecision = { line_id: 'line', line_revision: 9, item_revision: 11, action: 'void', replacement: null, reason: 'Void the incorrect attribution' } as const satisfies components['schemas']['PurchaseLineCorrectionWrite']
const lineReplacementDecision = { ...lineVoidDecision, action: 'replace', replacement: { item_id: 'other-item', item_revision: 17, item_unit_id: null, quantity_base: 2, paid_minor: 0 } } as const satisfies components['schemas']['PurchaseLineCorrectionWrite']
const correctionHeaders = { ...authHeaders, 'If-Match': '"9"' } satisfies operations['post_purchases_id_line_corrections']['parameters']['header']
const disposalRecordData = { item_unit_id: 'unit', kind: 'sold', currency: 'JPY', disposed_at: '2026-11-07T02:00:00.123456789Z', realized_proceeds_minor: 200, note: '' } as const satisfies components['schemas']['DisposalData']
const disposalRecord = { item_unit_id: 'unit', revision: 7, original: disposalRecordData, current: disposalRecordData } as const satisfies components['schemas']['DisposalRecord']
const voidedDisposalRecord = { ...disposalRecord, revision: 8, current: null } as const satisfies components['schemas']['DisposalRecord']
const disposalVoidDecision = { asset_revision: 13, action: 'void', replacement: null, reason: 'Void finance without reviving the Unit' } as const satisfies components['schemas']['DisposalCorrectionWrite']
const disposalReplacementDecision = { ...disposalVoidDecision, action: 'replace', replacement: { kind: 'other', currency: 'JPY', disposed_at: '1999-01-01T00:00:00Z', realized_proceeds_minor: 0 } } as const satisfies components['schemas']['DisposalCorrectionWrite']
const correctionOriginalLine = { id: 'line', revision: 9, purchase_id: 'purchase', item_id: 'item', item_unit_id: 'unit', quantity_base: 3, paid_minor: 100, note: '' } as const satisfies components['schemas']['PurchaseLine']
const lineVoidEntry = { id: 'correction', revision: 1, purchase_id: 'purchase', action: 'void', reason: 'Void the incorrect attribution', created_at: '2026-11-07T02:00:00.000000001Z', before: correctionOriginalLine, after: null } as const satisfies components['schemas']['PurchaseLineCorrection']
const lineReplacementEntry = { ...lineVoidEntry, action: 'replace', after: { ...correctionOriginalLine, id: 'new-line', revision: 1, item_unit_id: null, quantity_base: 2, paid_minor: 0 } } as const satisfies components['schemas']['PurchaseLineCorrection']
const disposalVoidEntry = { id: 'correction', revision: 1, item_unit_id: 'unit', action: 'void', reason: 'Void finance', created_at: '2026-11-07T02:00:00.000000001Z', before: disposalRecord, after: voidedDisposalRecord } as const satisfies components['schemas']['DisposalCorrection']
const disposalReplacementEntry = { ...disposalVoidEntry, action: 'replace', before: voidedDisposalRecord, after: { ...disposalRecord, revision: 9 } } as const satisfies components['schemas']['DisposalCorrection']
const correctionTransportCalls = [
  purchaseTransport.GET('/purchases/{id}/line-corrections', { params: { path: { id: 'purchase' }, query: { limit: 50 } } }),
  purchaseTransport.POST('/purchases/{id}/line-corrections', { params: { path: { id: 'purchase' }, header: correctionHeaders }, body: lineVoidDecision }),
  purchaseTransport.POST('/purchases/{id}/line-corrections', { params: { path: { id: 'purchase' }, header: correctionHeaders }, body: lineReplacementDecision }),
  purchaseTransport.GET('/units/{id}/disposal-record', { params: { path: { id: 'unit' } } }),
  purchaseTransport.GET('/units/{id}/disposal-corrections', { params: { path: { id: 'unit' }, query: { limit: 50, cursor: 'original-live-continuation' } } }),
  purchaseTransport.POST('/units/{id}/disposal-corrections', { params: { path: { id: 'unit' }, header: correctionHeaders }, body: disposalVoidDecision }),
  purchaseTransport.POST('/units/{id}/disposal-corrections', { params: { path: { id: 'unit' }, header: correctionHeaders }, body: disposalReplacementDecision }),
]
// @ts-expect-error A void needs explicit null, not an omitted replacement decision.
const implicitLineVoid: components['schemas']['PurchaseLineCorrectionWrite'] = { line_id: 'line', line_revision: 9, item_revision: 11, action: 'void', reason: 'Explicit reason' }
// @ts-expect-error Replacement cannot silently become void.
const nullLineReplacement: components['schemas']['PurchaseLineCorrectionWrite'] = { ...lineVoidDecision, action: 'replace' }
// @ts-expect-error The old Line proof does not replace the old Item interpretation proof.
const unprovenOldCorrectionItem: components['schemas']['PurchaseLineCorrectionWrite'] = { line_id: 'line', line_revision: 9, action: 'void', replacement: null, reason: 'Explicit reason' }
// @ts-expect-error Replacement keeps explicit nullable Unit intent through the actual typed transport.
const implicitReplacementUnit = purchaseTransport.POST('/purchases/{id}/line-corrections', { params: { path: { id: 'purchase' }, header: correctionHeaders }, body: { ...lineReplacementDecision, replacement: { item_id: 'item', item_revision: 11, quantity_base: 2, paid_minor: 0 } } })
// @ts-expect-error A named replacement Unit has quantity one, not a bulk allocation.
const bulkReplacementUnit = purchaseTransport.POST('/purchases/{id}/line-corrections', { params: { path: { id: 'purchase' }, header: correctionHeaders }, body: { ...lineReplacementDecision, replacement: { ...lineReplacementDecision.replacement, item_unit_id: 'unit', quantity_base: 2 } } })
// @ts-expect-error A real request marker object is never a null wire decision.
const markedLineVoid = purchaseTransport.POST('/purchases/{id}/line-corrections', { params: { path: { id: 'purchase' }, header: correctionHeaders }, body: { ...lineVoidDecision, replacement: { $write: null } } })
// @ts-expect-error Correction-entry reply revision is always its actual independent one.
const mutableLineCorrectionEntry: components['schemas']['PurchaseLineCorrection'] = { ...lineVoidEntry, revision: 2 }
// @ts-expect-error A replacement is a new Line with independent revision one.
const rebasedLineCorrectionReply: components['schemas']['PurchaseLineCorrection'] = { ...lineReplacementEntry, after: { ...lineReplacementEntry.after, revision: 10 } }
// @ts-expect-error Current disposal absence after void is explicit rather than omitted.
const implicitDisposalRecord: components['schemas']['DisposalRecord'] = { item_unit_id: 'unit', revision: 8, original: disposalRecordData }
// @ts-expect-error Original disposal source cannot be null or reconstructed from the current decision.
const absentOriginalDisposalRecord: components['schemas']['DisposalRecord'] = { ...voidedDisposalRecord, original: null }
// @ts-expect-error Pure financial correction does not acquire another physical Unit body proof.
const physicalDisposalCorrection: components['schemas']['DisposalCorrectionWrite'] = { ...disposalVoidDecision, unit_revision: 13 }
// @ts-expect-error Original Asset interpretation is independently required in the body.
const implicitCorrectionAsset: components['schemas']['DisposalCorrectionWrite'] = { action: 'void', replacement: null, reason: 'Explicit reason' }
// @ts-expect-error Replacement cannot mean null financial decision.
const nullDisposalReplacement: components['schemas']['DisposalCorrectionWrite'] = { ...disposalVoidDecision, action: 'replace' }
// @ts-expect-error Non-sale correction still requires literal zero proceeds in the actual client request.
const paidNonSaleCorrection = purchaseTransport.POST('/units/{id}/disposal-corrections', { params: { path: { id: 'unit' }, header: correctionHeaders }, body: { ...disposalReplacementDecision, replacement: { ...disposalReplacementDecision.replacement, realized_proceeds_minor: 1 } } })
// @ts-expect-error A real marker object is not a disposal void on the wire.
const markedDisposalVoid = purchaseTransport.POST('/units/{id}/disposal-corrections', { params: { path: { id: 'unit' }, header: correctionHeaders }, body: { ...disposalVoidDecision, replacement: { $write: null } } })
// @ts-expect-error Financial correction requires the original subject If-Match too.
const unprovenDisposalCorrection = purchaseTransport.POST('/units/{id}/disposal-corrections', { params: { path: { id: 'unit' }, header: authHeaders }, body: disposalVoidDecision })
// @ts-expect-error A void history entry must retain explicit null current state afterward.
const activeDisposalVoidReply: components['schemas']['DisposalCorrection'] = { ...disposalVoidEntry, after: disposalRecord }
// @ts-expect-error Reinstatement must retain an actual complete current financial decision.
const nullDisposalReplacementReply: components['schemas']['DisposalCorrection'] = { ...disposalVoidEntry, action: 'replace' }
// @ts-expect-error Live history pages require an explicit terminal continuation.
const incompleteCorrectionPage: components['schemas']['PurchaseLineCorrectionList'] = { data: [lineVoidEntry] }
void [disposalRecord, voidedDisposalRecord, lineVoidEntry, lineReplacementEntry, disposalVoidEntry, disposalReplacementEntry, correctionTransportCalls,
  implicitLineVoid, nullLineReplacement, unprovenOldCorrectionItem, implicitReplacementUnit, bulkReplacementUnit, markedLineVoid,
  mutableLineCorrectionEntry, rebasedLineCorrectionReply, implicitDisposalRecord, absentOriginalDisposalRecord, physicalDisposalCorrection,
  implicitCorrectionAsset, nullDisposalReplacement, paidNonSaleCorrection, markedDisposalVoid, unprovenDisposalCorrection,
  activeDisposalVoidReply, nullDisposalReplacementReply, incompleteCorrectionPage]

// Financial correction adapters retain every original source, never loose counters.
declare const financialSDK: typeof import('../src/lib/api/financial-corrections')
const originalCorrectionItem = { ...currentItem, id: 'item', revision: 11 }
const replacementCorrectionItem = { ...currentItem, id: 'other-item', revision: 17, tracking_mode: 'quantity' as const }
const purchaseCorrectionContext = { ...writeContext, purchase: reviewedPurchase, line: correctionOriginalLine, item: originalCorrectionItem, replacementItem: null }
const replacementCorrectionContext = { ...purchaseCorrectionContext, replacementItem: replacementCorrectionItem }
const disposalCorrectionContext = { ...writeContext, disposal: disposalRecord, asset: { ...retainedAsset, revision: 13 } }
const correctionSDKCalls = [
  financialSDK.fetchPurchaseCorrectionsPage('purchase', { limit: 50 }, new AbortController().signal),
  financialSDK.correctPurchaseLine('purchase', lineVoidDecision, purchaseCorrectionContext),
  financialSDK.correctPurchaseLine('purchase', lineReplacementDecision, replacementCorrectionContext),
  financialSDK.fetchDisposalRecord('unit', new AbortController().signal),
  financialSDK.fetchDisposalCorrectionsPage('unit', {}, new AbortController().signal),
  financialSDK.correctDisposal('unit', disposalVoidDecision, disposalCorrectionContext),
  financialSDK.correctDisposal('unit', disposalReplacementDecision, { ...disposalCorrectionContext, disposal: voidedDisposalRecord }),
]
// @ts-expect-error A reviewed Purchase alone is not the original Line/Item context.
const ownerlessSDKCorrection = financialSDK.correctPurchaseLine('purchase', lineVoidDecision, { ...writeContext, purchase: reviewedPurchase })
// @ts-expect-error Void still explicitly records the absence of a replacement Item.
const implicitSDKReplacementContext = financialSDK.correctPurchaseLine('purchase', lineVoidDecision, { ...writeContext, purchase: reviewedPurchase, line: correctionOriginalLine, item: originalCorrectionItem })
// @ts-expect-error A loose or substituted counter cannot override the reviewed Purchase.
const overriddenSDKCorrectionProof = financialSDK.correctPurchaseLine('purchase', lineVoidDecision, { ...purchaseCorrectionContext, expectedRevision: 9 })
// @ts-expect-error Void retains a literal-null replacement rather than a marker.
const markedSDKCorrection = financialSDK.correctPurchaseLine('purchase', { ...lineVoidDecision, replacement: { $write: null } }, purchaseCorrectionContext)
// @ts-expect-error A replacement must explicitly state its nullable Unit intent.
const implicitSDKCorrectionUnit = financialSDK.correctPurchaseLine('purchase', { ...lineReplacementDecision, replacement: { item_id: 'other-item', item_revision: 17, quantity_base: 2, paid_minor: 0 } }, replacementCorrectionContext)
// @ts-expect-error A named replacement Unit cannot carry a bulk quantity.
const bulkSDKCorrectionUnit = financialSDK.correctPurchaseLine('purchase', { ...lineReplacementDecision, replacement: { ...lineReplacementDecision.replacement, item_unit_id: 'unit', quantity_base: 2 } }, replacementCorrectionContext)
// @ts-expect-error Financial disposal correction retains the actual record, not a loose proof.
const ownerlessSDKDisposalCorrection = financialSDK.correctDisposal('unit', disposalVoidDecision, { ...writeContext, asset: retainedAsset, expectedRevision: 7 })
// @ts-expect-error The original Asset is independently required with the DisposalRecord.
const assetlessSDKDisposalCorrection = financialSDK.correctDisposal('unit', disposalVoidDecision, { ...writeContext, disposal: disposalRecord })
// @ts-expect-error Disposal financial writes never acquire an extra physical proof.
const physicalSDKDisposalCorrection = financialSDK.correctDisposal('unit', { ...disposalVoidDecision, unit_revision: 13 }, disposalCorrectionContext)
// @ts-expect-error Non-sale replacement cannot carry nonzero proceeds.
const paidSDKNonSaleCorrection = financialSDK.correctDisposal('unit', { ...disposalReplacementDecision, replacement: { ...disposalReplacementDecision.replacement, realized_proceeds_minor: 1 } }, disposalCorrectionContext)
// @ts-expect-error Owned correction history accepts paging only.
const filteredSDKCorrectionHistory = financialSDK.fetchDisposalCorrectionsPage('unit', { currency: 'JPY' }, new AbortController().signal)
void [correctionSDKCalls, ownerlessSDKCorrection, implicitSDKReplacementContext, overriddenSDKCorrectionProof, markedSDKCorrection,
  implicitSDKCorrectionUnit, bulkSDKCorrectionUnit, ownerlessSDKDisposalCorrection, assetlessSDKDisposalCorrection,
  physicalSDKDisposalCorrection, paidSDKNonSaleCorrection, filteredSDKCorrectionHistory]

// Bracketed correction read models distinguish unqueried, quantity and individual sources.
declare const purchaseCorrectionModels: typeof import('../src/features/purchases/correction-model')
declare const disposalCorrectionModels: typeof import('../src/features/assets/correction-model')
const correctionHistorySource = { purchase: reviewedPurchase, lines: [correctionOriginalLine], corrections: [] } satisfies import('../src/features/purchases/correction-model').PurchaseCorrectionHistory
const correctionSelectionSource = { ...correctionHistorySource, line: correctionOriginalLine, item: originalCorrectionItem,
  originalUnit: { kind: 'resolved', unit: holdingUnit }, replacement: { kind: 'quantity', item: replacementCorrectionItem } } as const satisfies import('../src/features/purchases/correction-model').PurchaseCorrectionSource
const disposalCorrectionSource = { unit: holdingUnit, item: originalCorrectionItem, asset: retainedAsset, disposal: voidedDisposalRecord, corrections: [disposalVoidEntry] } satisfies import('../src/features/assets/correction-model').DisposalCorrectionSource
const correctionReadCalls = [purchaseCorrectionModels.readPurchaseCorrectionHistory('purchase', new AbortController().signal),
  purchaseCorrectionModels.readPurchaseCorrection('purchase', 'line', null, new AbortController().signal),
  purchaseCorrectionModels.readPurchaseCorrection('purchase', 'line', 'replacement-item', new AbortController().signal),
  disposalCorrectionModels.readDisposalCorrection('unit', new AbortController().signal)]
// @ts-expect-error The replacement decision must be explicit even when there is no target.
const implicitCorrectionSelection = purchaseCorrectionModels.readPurchaseCorrection('purchase', 'line', undefined, new AbortController().signal)
// @ts-expect-error Quantity selection cannot pretend that a Unit collection was queried.
const inventedCorrectionUnits: import('../src/features/purchases/correction-model').ReplacementCorrectionSource = { kind: 'quantity', item: replacementCorrectionItem, units: [] }
// @ts-expect-error An individual replacement needs its complete selected Unit collection.
const incompleteCorrectionUnits: import('../src/features/purchases/correction-model').ReplacementCorrectionSource = { kind: 'individual', item: originalCorrectionItem }
// @ts-expect-error A path-unaddressable original Unit remains an explicit identity, not null.
const missingLegacyCorrectionUnit: import('../src/features/purchases/correction-model').OriginalCorrectionUnit = { kind: 'unaddressable' }
// @ts-expect-error A correction source cannot omit the actual retained DisposalRecord.
const missingCorrectionRecord: import('../src/features/assets/correction-model').DisposalCorrectionSource = { unit: holdingUnit, item: originalCorrectionItem, asset: retainedAsset, corrections: [] }
void [correctionSelectionSource, disposalCorrectionSource, correctionReadCalls, implicitCorrectionSelection, inventedCorrectionUnits, incompleteCorrectionUnits, missingLegacyCorrectionUnit, missingCorrectionRecord]

// Raw financial correction values stay text until an explicit reviewed proposal.
declare const purchaseCorrectionForms: typeof import('../src/features/purchases/correction-form')
declare const disposalCorrectionForms: typeof import('../src/features/assets/correction-form')
const rawPurchaseCorrection = { action: 'replace', reason: 'Reason', replacement_item_id: 'other-item', allocation: 'none', item_unit_id: '', quantity_base: '2', paid_minor: '0', note: '' } satisfies import('../src/features/purchases/correction-form').PurchaseCorrectionValues
const rawDisposalCorrection = { action: 'replace', reason: 'Reason', kind: 'sold', currency: 'JPY', realized_proceeds_minor: '0', disposed_at: '1999-01-01T00:00:00Z', note: '' } satisfies import('../src/features/assets/correction-form').DisposalCorrectionValues
const correctionFormCalls = [purchaseCorrectionForms.purchaseCorrectionDefaults(), purchaseCorrectionForms.purchaseCorrectionProposal(rawPurchaseCorrection, correctionSelectionSource),
  purchaseCorrectionForms.adoptPurchaseCorrection(rawPurchaseCorrection, correctionSelectionSource, correctionSelectionSource), disposalCorrectionForms.disposalCorrectionDefaults(disposalCorrectionSource),
  disposalCorrectionForms.disposalCorrectionProposal(rawDisposalCorrection, disposalCorrectionSource), disposalCorrectionForms.adoptDisposalCorrection(rawDisposalCorrection, disposalCorrectionSource, disposalCorrectionSource)]
const reviewedVoidProposal = { source: correctionSelectionSource, body: lineVoidDecision, unit: null } satisfies import('../src/features/purchases/correction-form').PurchaseCorrectionProposal
const reviewedReplacementProposal = { source: correctionSelectionSource, body: lineReplacementDecision, unit: null } satisfies import('../src/features/purchases/correction-form').PurchaseCorrectionProposal
const reviewedDisposalProposal = { source: disposalCorrectionSource, body: disposalReplacementDecision } satisfies import('../src/features/assets/correction-form').DisposalCorrectionProposal
// @ts-expect-error New amounts remain raw text, not prematurely coerced numbers.
const numericCorrectionDraft: import('../src/features/purchases/correction-form').PurchaseCorrectionValues = { ...rawPurchaseCorrection, paid_minor: 0 }
// @ts-expect-error The nullable replacement Unit decision is an explicit draft field.
const missingCorrectionUnitDraft: import('../src/features/purchases/correction-form').PurchaseCorrectionValues = { action: 'replace', reason: '', replacement_item_id: '', quantity_base: '', paid_minor: '', note: '' }
// @ts-expect-error A void cannot retain a replacement Unit as its proposed selection.
const physicalVoidProposal: import('../src/features/purchases/correction-form').PurchaseCorrectionProposal = { source: correctionSelectionSource, body: lineVoidDecision, unit: holdingUnit }
// @ts-expect-error A reviewed proposal retains its complete original source, not loose proofs.
const sourcelessDisposalProposal: import('../src/features/assets/correction-form').DisposalCorrectionProposal = { body: disposalVoidDecision }
// @ts-expect-error Disposal proceeds are raw text until reviewed.
const numericDisposalDraft: import('../src/features/assets/correction-form').DisposalCorrectionValues = { ...rawDisposalCorrection, realized_proceeds_minor: 0 }
void [correctionFormCalls, reviewedVoidProposal, reviewedReplacementProposal, reviewedDisposalProposal, numericCorrectionDraft, missingCorrectionUnitDraft, physicalVoidProposal, sourcelessDisposalProposal, numericDisposalDraft]

// Correction commands freeze complete original sources and cannot attach credentials or invented proofs.
declare const correctionCommands: typeof import('../src/features/financial-corrections/commands')
const purchaseCorrectionCommand = correctionCommands.freezeCorrectionCommand({ kind: 'correct-purchase', ...reviewedReplacementProposal })
const disposalCorrectionCommand = correctionCommands.freezeCorrectionCommand({ kind: 'correct-disposal', ...reviewedDisposalProposal })
const sendCorrectionCalls = [correctionCommands.sendCorrectionCommand(purchaseCorrectionCommand, 'csrf', new AbortController().signal),
  correctionCommands.sendCorrectionCommand(disposalCorrectionCommand, 'csrf', new AbortController().signal)]
// @ts-expect-error A new review cannot supply a reused original key.
const reusedCorrectionKey = correctionCommands.freezeCorrectionCommand({ kind: 'correct-purchase', ...reviewedVoidProposal, key: 'previous-key' })
// @ts-expect-error Credentials are caller-owned and cannot be retained in a command.
const credentialCorrectionCommand = correctionCommands.freezeCorrectionCommand({ kind: 'correct-disposal', ...reviewedDisposalProposal, csrfToken: 'secret' })
// @ts-expect-error A financial void cannot invent a selected physical Unit.
const physicalCorrectionCommand = correctionCommands.freezeCorrectionCommand({ kind: 'correct-purchase', ...reviewedVoidProposal, unit: holdingUnit })
// @ts-expect-error A command must retain the complete reviewed original source.
const unreviewedCorrectionCommand = correctionCommands.freezeCorrectionCommand({ kind: 'correct-disposal', body: disposalVoidDecision })
// @ts-expect-error A subject proof or caller key alone is not a frozen command.
const looseCorrectionSend = correctionCommands.sendCorrectionCommand({ kind: 'correct-disposal', body: disposalVoidDecision, key: 'previous-key' }, 'csrf', new AbortController().signal)
void [sendCorrectionCalls, reusedCorrectionKey, credentialCorrectionCommand, physicalCorrectionCommand, unreviewedCorrectionCommand, looseCorrectionSend]

// Shared media identities, owner intent and independent proofs remain explicit.
const mediaMetadata = { id: 'media', revision: 4, sha256: 'a'.repeat(64), mime_type: 'image/jpeg', verification: 'verified', width: 4000, height: 3000, bytes: 120000,
  thumb_url: null, thumb_sha256: null } as const satisfies components['schemas']['Media']
const unverifiedMediaMetadata = { ...mediaMetadata, mime_type: 'image/heic', verification: 'unverified' } as const satisfies components['schemas']['Media']
const uploadedMedia = mediaMetadata satisfies operations['post_media']['responses'][200]['content']['application/json']
// @ts-expect-error A fresh upload cannot return an unverified legacy record.
const unverifiedUpload: operations['post_media']['responses'][200]['content']['application/json'] = unverifiedMediaMetadata
// @ts-expect-error Upload responses require one of the explicitly supported verified image formats.
const unsupportedUpload: operations['post_media']['responses'][200]['content']['application/json'] = { ...mediaMetadata, mime_type: 'image/heic' }
// @ts-expect-error Readable historical metadata must carry its verification status.
const unknownMediaStatus: components['schemas']['Media'] = { id: 'media', revision: 4, sha256: 'a'.repeat(64), mime_type: 'image/jpeg', width: 1, height: 1, bytes: 1, thumb_url: null, thumb_sha256: null }
const mediaLink = { id: 'link', revision: 1, media_id: 'media', owner_type: 'item', owner_id: 'item', is_cover: false,
  sort_order: -1, description: '' } satisfies components['schemas']['MediaLink']
const mediaLinkView = { ...mediaLink, media: mediaMetadata } satisfies components['schemas']['MediaLinkView']
const mediaPage = { data: [mediaLinkView], next_cursor: null } satisfies components['schemas']['MediaLinkList']
const mediaOwnerInputs = [
  { item_id: 'item', owner_revision: 3 },
  { item_unit_id: 'unit', owner_revision: 4, is_cover: true },
  { location_id: 'location', owner_revision: 5, sort_order: 0, description: '' },
] as const satisfies readonly components['schemas']['MediaLinkCreate'][]
const mediaHeaders = { ...authHeaders, 'If-Match': '"4"' } satisfies operations['post_media_id_links']['parameters']['header']
const mediaTransportCalls = [
  purchaseTransport.GET('/media/{id}/view', { params: { path: { id: 'links' } } }),
  purchaseTransport.GET('/media/{id}/links', { params: { path: { id: 'media' }, query: { limit: 50 } } }),
  purchaseTransport.GET('/media/links', { params: { query: { owner_type: 'item', owner_id: 'item' } } }),
  purchaseTransport.POST('/media/{id}/links', { params: { path: { id: 'media' }, header: mediaHeaders }, body: mediaOwnerInputs[0] }),
  purchaseTransport.POST('/media/{id}/links', { params: { path: { id: 'media' }, header: mediaHeaders }, body: mediaOwnerInputs[1] }),
  purchaseTransport.POST('/media/{id}/links', { params: { path: { id: 'media' }, header: mediaHeaders }, body: mediaOwnerInputs[2] }),
  purchaseTransport.PATCH('/media/links/{id}', { params: { path: { id: 'link' }, header: mediaHeaders }, body: { description: '' } }),
  purchaseTransport.DELETE('/media/links/{id}', { params: { path: { id: 'link' }, header: mediaHeaders } }),
  purchaseTransport.DELETE('/media/{id}', { params: { path: { id: 'media' }, header: mediaHeaders } }),
]
// @ts-expect-error Attachment needs the original selected owner's independent revision.
const unprovenMediaOwner: components['schemas']['MediaLinkCreate'] = { item_id: 'item' }
// @ts-expect-error The owner must be explicit even when the Media is known.
const ownerlessMediaLink: components['schemas']['MediaLinkCreate'] = { owner_revision: 3 }
// @ts-expect-error Exactly one actual owner is accepted, not a synthetic multi-owner link.
const multipleMediaOwners: components['schemas']['MediaLinkCreate'] = { item_id: 'item', location_id: 'location', owner_revision: 3 }
// @ts-expect-error The generated direct transport preserves the same exclusive owner requirement.
const multipleRawMediaOwners = purchaseTransport.POST('/media/{id}/links', { params: { path: { id: 'media' }, header: mediaHeaders }, body: { item_id: 'item', item_unit_id: 'unit', owner_revision: 3 } })
// @ts-expect-error Direct transport cannot omit the owner's independent body proof.
const unprovenRawMediaOwner = purchaseTransport.POST('/media/{id}/links', { params: { path: { id: 'media' }, header: mediaHeaders }, body: { item_id: 'item' } })
// @ts-expect-error The Media's original If-Match is separate from its owner's body proof.
const unprovenMediaHeaders: operations['post_media_id_links']['parameters']['header'] = { ...authHeaders }
// @ts-expect-error A row ETag cannot cover independently changing embedded Media metadata.
const embeddedMediaResource: components['schemas']['MediaLink'] = { ...mediaLink, media: mediaMetadata }
// @ts-expect-error Responses retain the complete selected owner, not only a link ID.
const partialMediaLink: components['schemas']['MediaLink'] = { id: 'link', revision: 1 }
// @ts-expect-error List views need actual separately versioned Media metadata.
const incompleteMediaView: components['schemas']['MediaLinkView'] = { ...mediaLink }
// @ts-expect-error List views cannot expose unrelated raw OCR alongside the joined metadata.
const unsafeMediaView: components['schemas']['MediaLinkView'] = { ...mediaLinkView, raw_ocr: 'unreviewed' }
// @ts-expect-error Live collections explicitly terminate or continue.
const partialMediaPage: components['schemas']['MediaLinkList'] = { data: [] }
// @ts-expect-error Owner-scoped lists require the actual owner identity.
const missingMediaOwnerFilter: operations['get_media_links']['parameters']['query'] = { owner_type: 'item' }
// @ts-expect-error A link patch cannot reassign its immutable Media.
const reassignedMediaLink: components['schemas']['MediaLinkWrite'] = { media_id: 'other' }
void [mediaPage, mediaOwnerInputs, mediaTransportCalls, uploadedMedia, unverifiedUpload, unsupportedUpload, unknownMediaStatus, unprovenMediaOwner, ownerlessMediaLink, multipleMediaOwners,
  multipleRawMediaOwners, unprovenRawMediaOwner, unprovenMediaHeaders, embeddedMediaResource, partialMediaLink, incompleteMediaView, unsafeMediaView, partialMediaPage, missingMediaOwnerFilter, reassignedMediaLink]

// Browser reads use explicit scope, return independent Media and link views and
// never invent a client-side owner or allow upload/write arguments.
declare const mediaAPI: typeof import('../src/lib/api/media')
const mediaRead: Promise<components['schemas']['Media']> = mediaAPI.fetchMedia('links', new AbortController().signal)
const mediaLinksRead: Promise<components['schemas']['MediaLinkList']> = mediaAPI.fetchMediaLinksPage('media', { limit: 50 }, new AbortController().signal)
const ownerLinksRead: Promise<components['schemas']['MediaLinkList']> = mediaAPI.fetchOwnerMediaLinksPage('unit', 'unit-id', {}, new AbortController().signal)
// @ts-expect-error Purchase is not a supported MediaLink owner kind.
const unsupportedMediaOwner = mediaAPI.fetchOwnerMediaLinksPage('purchase', 'purchase-id', {}, new AbortController().signal)
// @ts-expect-error Scoped link pages cannot silently substitute another owner.
const overriddenMediaOwner = mediaAPI.fetchOwnerMediaLinksPage('item', 'item-id', { owner_id: 'other' }, new AbortController().signal)
// @ts-expect-error Reads need an explicit caller-owned cancellation signal.
const uncancellableMediaRead = mediaAPI.fetchMedia('media')
// @ts-expect-error A link row has no embedded Media covered by its ETag.
const embeddedMediaRow = mediaLink.media
void [mediaRead, mediaLinksRead, ownerLinksRead, unsupportedMediaOwner, overriddenMediaOwner, uncancellableMediaRead, embeddedMediaRow]

// Media writes retain complete reviewed source; callers cannot replace those
// proofs with unrelated loose counters or change a link's immutable owner.
declare const mediaWrites: typeof import('../src/lib/api/media-writes')
const mediaAttachContext = { ...writeContext, media: mediaMetadata, owner: { kind: 'item' as const, value: currentItem } }
const unitMediaAttachContext = { ...writeContext, media: unverifiedMediaMetadata, owner: { kind: 'unit' as const, value: holdingUnit } }
const locationMediaOwner = { id: 'location', revision: 5, parent_id: null, name: 'Room', code: 'ROOM', kind: 'room', status: 'active', sort_order: 0, description: '' } satisfies components['schemas']['Location']
const locationMediaAttachContext = { ...writeContext, media: mediaMetadata, owner: { kind: 'location' as const, value: locationMediaOwner } }
const mediaRowContext = { ...writeContext, link: mediaLink }
const mediaViewContext = { ...writeContext, link: mediaLinkView }
const mediaWriteCalls = [
  mediaWrites.attachMedia('media', {}, mediaAttachContext),
  mediaWrites.attachMedia('media', { is_cover: false, sort_order: 0, description: '' }, unitMediaAttachContext),
  mediaWrites.attachMedia('media', { sort_order: 1 }, locationMediaAttachContext),
  mediaWrites.patchMediaLink('link', { description: '' }, mediaViewContext),
  mediaWrites.patchMediaLink('link', { is_cover: true, sort_order: 0 }, mediaRowContext),
  mediaWrites.unlinkMediaLink('link', mediaRowContext),
]
// @ts-expect-error Attachment needs an original Media snapshot, not a loose revision.
const mediaAttachWithoutSnapshot = mediaWrites.attachMedia('media', {}, { ...writeContext, owner: mediaAttachContext.owner, expectedRevision: 4 })
// @ts-expect-error Attachment needs one complete, explicitly typed original owner.
const mediaAttachWithoutOwner = mediaWrites.attachMedia('media', {}, { ...writeContext, media: mediaMetadata })
// @ts-expect-error A Unit cannot be passed as an Item owner snapshot.
const mediaAttachWrongOwner = mediaWrites.attachMedia('media', {}, { ...writeContext, media: mediaMetadata, owner: { kind: 'item', value: holdingUnit } })
// @ts-expect-error Callers cannot supply a competing loose Media proof.
const mediaAttachLooseRevision = mediaWrites.attachMedia('media', {}, { ...mediaAttachContext, expectedRevision: 4 })
// @ts-expect-error Owner identity and revision are derived from the same original snapshot.
const mediaAttachInjectedOwner = mediaWrites.attachMedia('media', { item_id: 'another', owner_revision: 1 }, mediaAttachContext)
// @ts-expect-error A partial metadata edit must explicitly select at least one field.
const emptyMediaLinkPatch = mediaWrites.patchMediaLink('link', {}, mediaRowContext)
// @ts-expect-error A link edit cannot reassign its immutable owner or Media.
const reassignMediaLinkPatch = mediaWrites.patchMediaLink('link', { owner_id: 'another' }, mediaRowContext)
// @ts-expect-error A joined source or complete independent link is needed for edit review.
const unreviewedMediaLinkPatch = mediaWrites.patchMediaLink('link', { description: '' }, { ...writeContext, expectedRevision: 1 })
// @ts-expect-error Unlinking binds its own reviewed row, not a Media proof.
const unreviewedMediaUnlink = mediaWrites.unlinkMediaLink('link', { ...writeContext, media: mediaMetadata })
// @ts-expect-error The original link proof cannot be silently overridden.
const looseMediaUnlink = mediaWrites.unlinkMediaLink('link', { ...mediaRowContext, expectedRevision: 2 })
void [mediaWriteCalls, mediaAttachWithoutSnapshot, mediaAttachWithoutOwner, mediaAttachWrongOwner, mediaAttachLooseRevision, mediaAttachInjectedOwner,
  emptyMediaLinkPatch, reassignMediaLinkPatch, unreviewedMediaLinkPatch, unreviewedMediaUnlink, looseMediaUnlink]

// Complete live review scopes retain only the selected actual source and links.
declare const mediaModels: typeof import('../src/features/media/model')
const reviewedItemMedia: Promise<import('../src/features/media/model').OwnerMedia> = mediaModels.readOwnerMedia({ kind: 'item', id: currentItem.id }, new AbortController().signal)
const reviewedUnitMedia = mediaModels.readOwnerMedia({ kind: 'unit', id: holdingUnit.id, itemID: currentItem.id }, new AbortController().signal)
const reviewedLocationMedia = mediaModels.readOwnerMedia({ kind: 'location', id: locationMediaOwner.id }, new AbortController().signal)
const reviewedMediaMembership: Promise<import('../src/features/media/model').MediaMembership> = mediaModels.readMediaMembership(mediaMetadata.id, new AbortController().signal)
// @ts-expect-error Purchase is not an implemented MediaLink owner.
const unsupportedMediaScope = mediaModels.readOwnerMedia({ kind: 'purchase', id: 'purchase' }, new AbortController().signal)
// @ts-expect-error Owner scope must identify the actual selected owner.
const implicitMediaScope = mediaModels.readOwnerMedia({ kind: 'item' }, new AbortController().signal)
// @ts-expect-error Item parent hints cannot be attached to a Location scope.
const unrelatedMediaScope = mediaModels.readOwnerMedia({ kind: 'location', id: 'location', itemID: 'item' }, new AbortController().signal)
// @ts-expect-error Every live review read retains caller-owned cancellation.
const uncancellableMediaScope = mediaModels.readMediaMembership('media')
void [reviewedItemMedia, reviewedUnitMedia, reviewedLocationMedia, reviewedMediaMembership, unsupportedMediaScope, implicitMediaScope, unrelatedMediaScope, uncancellableMediaScope]
