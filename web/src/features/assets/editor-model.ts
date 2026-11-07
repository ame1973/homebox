import type { FieldErrors, Resolver } from 'react-hook-form'
import { ApiError, validText } from '@/lib/api/client'
import { input, parseSafeCount } from '@/lib/api/domain'
import { canonicalInstant, purchaseInstantSchema } from '@/lib/api/purchases'
import { messages } from '@/lib/messages'
import type { AssetCommandInput } from './commands'
import type { AssetCostsSource, AssetLifecycleSource, AssetProfileSource, AssetUsageSource } from './model'
import * as forms from './form'

export type AssetEditorData = Readonly<{ kind: 'put-profile'; source: AssetProfileSource }>
  | Readonly<{ kind: 'record-usage'; source: AssetUsageSource }>
  | Readonly<{ kind: 'record-cost'; source: AssetCostsSource }>
  | Readonly<{ kind: 'retire-asset'; source: AssetLifecycleSource }>
  | Readonly<{ kind: 'dispose-asset'; source: AssetLifecycleSource }>
export type AssetEditorValues = Record<string, string>
const text = messages.assets
export const editorFields = {
  'put-profile': ['cost_basis', 'currency', 'manual_acquisition_minor', 'usage_unit', 'service_decision', 'in_service_at', 'retirement_decision', 'retired_at', 'residual_decision', 'estimated_residual_minor'],
  'record-usage': ['event_type', 'delta', 'usage_unit', 'occurred_at', 'correction_of', 'note'],
  'record-cost': ['kind', 'direction', 'amount_minor', 'currency', 'occurred_at', 'correction_of', 'note'],
  'retire-asset': ['retired_at', 'note'],
  'dispose-asset': ['kind', 'currency', 'realized_proceeds_minor', 'disposed_at', 'note'],
} as const
export function assetEditorDefaults(data: AssetEditorData): AssetEditorValues {
  switch (data.kind) {
    case 'put-profile': return forms.assetProfileDefaults(data.source)
    case 'record-usage': return forms.assetUsageDefaults(data.source)
    case 'record-cost': return forms.assetCostDefaults(data.source)
    case 'retire-asset': return forms.assetRetireDefaults(data.source)
    case 'dispose-asset': return forms.assetDisposalDefaults(data.source)
  }
}
export function assetEditorProposal(data: AssetEditorData, values: AssetEditorValues): AssetCommandInput {
  switch (data.kind) {
    case 'put-profile': return { ...data, body: forms.assetProfileBody(input(forms.profileValues, values), data.source) }
    case 'record-usage': return { ...data, body: forms.assetUsageBody(input(forms.usageValues, values), data.source) }
    case 'record-cost': return { ...data, body: forms.assetCostBody(input(forms.costValues, values), data.source) }
    case 'retire-asset': return { ...data, body: forms.assetRetireBody(input(forms.retireValues, values), data.source) }
    case 'dispose-asset': return { ...data, body: forms.assetDisposalBody(input(forms.disposalValues, values), data.source) }
  }
}
export function retainAssetEditorEdits(values: AssetEditorValues, before: AssetEditorData, current: AssetEditorData): AssetEditorValues {
  switch (current.kind) {
    case 'put-profile': {
      if (before.kind !== 'put-profile') throw new ApiError('VALIDATION_ERROR')
      return forms.retainAssetProfileEdits(input(forms.profileValues, values), before.source, current.source)
    }
    case 'record-usage': {
      if (before.kind !== 'record-usage') throw new ApiError('VALIDATION_ERROR')
      return forms.adoptAssetUsage(input(forms.usageValues, values), before.source, current.source).values
    }
    case 'record-cost': {
      if (before.kind !== 'record-cost') throw new ApiError('VALIDATION_ERROR')
      return forms.adoptAssetCost(input(forms.costValues, values), before.source, current.source).values
    }
    case 'retire-asset': {
      if (before.kind !== 'retire-asset') throw new ApiError('VALIDATION_ERROR')
      return forms.retainAssetRetireEdits(input(forms.retireValues, values), before.source, current.source)
    }
    case 'dispose-asset': {
      if (before.kind !== 'dispose-asset') throw new ApiError('VALIDATION_ERROR')
      return forms.retainAssetDisposalEdits(input(forms.disposalValues, values), before.source, current.source)
    }
  }
}
export function assetFieldVisible(kind: AssetEditorData['kind'], name: string, values: Readonly<Partial<AssetEditorValues>>) {
  if (kind === 'put-profile') {
    if (name === 'manual_acquisition_minor') return values.cost_basis === 'manual'
    if (name === 'in_service_at') return values.service_decision === 'value'
    if (name === 'retired_at') return values.retirement_decision === 'value'
    if (name === 'estimated_residual_minor') return values.residual_decision === 'value'
  }
  if (name === 'correction_of') return kind === 'record-usage' ? values.event_type === 'correction' : values.kind === 'adjustment'
  return true
}

// Field feedback reuses the canonical scalar validators and the actual proposal
// builder. Complete ledger totals/prefixes remain authoritative in SQL.
export function assetEditorResolver(data: AssetEditorData): Resolver<AssetEditorValues> {
  return (values) => {
    const errors: FieldErrors<AssetEditorValues> = {}
    const check = (name: string, valid: boolean, message: string) => { if (!valid) errors[name] = { type: 'validate', message } }
    const instant = (name: string) => check(name, purchaseInstantSchema.safeParse(values[name]).success, text.instantError)
    const money = (name: string, minimum: 0 | 1 = 0) => check(name, parseSafeCount(values[name] ?? '', minimum) !== undefined, text.moneyError)
    if ('currency' in values) check('currency', /^[A-Z]{3}(?![\s\S])/.test(values.currency ?? '') && (data.kind === 'put-profile' || values.currency === data.source.asset?.currency), text.currencyHelp)
    if ('usage_unit' in values) check('usage_unit', ['times', 'minutes', 'cycles'].includes(values.usage_unit ?? '') && (data.kind === 'put-profile' || values.usage_unit === data.source.asset?.usage_unit), text.choiceError)
    if (data.kind === 'put-profile') {
      check('cost_basis', values.cost_basis === 'manual' || values.cost_basis === 'purchase', text.choiceError)
      if (values.cost_basis === 'manual') money('manual_acquisition_minor')
      for (const name of ['service_decision', 'retirement_decision', 'residual_decision']) check(name, values[name] === 'unknown' || values[name] === 'value', text.choiceError)
      if (values.service_decision === 'value') instant('in_service_at')
      if (values.retirement_decision === 'value') instant('retired_at')
      if (values.residual_decision === 'value') money('estimated_residual_minor')
      const service = canonicalInstant(values.in_service_at ?? ''), retired = canonicalInstant(values.retired_at ?? '')
      if (values.service_decision === 'value' && values.retirement_decision === 'value' && service !== undefined && retired !== undefined) check('in_service_at', service <= retired, text.instantError)
    } else if (data.kind === 'record-usage') {
      check('event_type', values.event_type === 'use' || values.event_type === 'correction', text.choiceError)
      const raw = values.delta ?? '', negative = raw.startsWith('-')
      check('delta', parseSafeCount(negative ? raw.slice(1) : raw, 1) !== undefined && (values.event_type === 'correction' || !negative), text.usageError)
      instant('occurred_at')
      if (values.event_type === 'correction') check('correction_of', data.source.usage.some((event) => event.id === values.correction_of && event.item_unit_id === data.source.unit.id && event.usage_unit === data.source.asset.usage_unit), text.targetError)
    } else if (data.kind === 'record-cost') {
      check('kind', Object.hasOwn(text.costKinds, values.kind ?? ''), text.choiceError)
      check('direction', values.direction === 'expense' || values.direction === 'credit', text.choiceError)
      money('amount_minor', 1); instant('occurred_at')
      if (values.kind === 'adjustment') check('correction_of', data.source.costs.some((event) => event.id === values.correction_of && event.item_unit_id === data.source.unit.id && event.currency === data.source.asset.currency), text.targetError)
    } else if (data.kind === 'retire-asset') instant('retired_at')
    else {
      check('kind', Object.hasOwn(text.disposalKinds, values.kind ?? ''), text.choiceError)
      money('realized_proceeds_minor'); instant('disposed_at')
      if (values.kind !== 'sold') check('realized_proceeds_minor', values.realized_proceeds_minor === '0', text.nonSaleError)
    }
    if ('note' in values) check('note', validText(values.note ?? '', 0, 10000), text.textHelp)
    if (Object.keys(errors).length) return { values: {}, errors }
    try { assetEditorProposal(data, values) } catch (error) {
      const key = data.kind === 'put-profile' ? 'retirement_decision' : data.kind === 'retire-asset' ? 'retired_at' : editorFields[data.kind][0]
      errors[key] = { type: 'validate', message: error instanceof ApiError && error.code === 'ASSET_HISTORY_LOCKED' ? text.historyLocked : text.instantError }
      return { values: {}, errors }
    }
    return { values, errors: {} }
  }
}
