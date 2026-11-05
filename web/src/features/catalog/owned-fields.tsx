import { ownedFields, type OwnedFieldsValue } from './display'

// Escaped string representations keep exact whitespace, control bytes and
// leading zeroes visible without interpreting barcode content as a URL or HTML.
export function OwnedFields({ value }: { value: OwnedFieldsValue }) {
  return <dl className="grid min-w-0 gap-4 sm:grid-cols-2">{ownedFields(value).map((field) => <div key={field.key} className="min-w-0 space-y-1"><dt className="text-sm font-medium text-muted-foreground">{field.label}</dt><dd className="whitespace-pre-wrap text-base [overflow-wrap:anywhere]">{field.value}</dd></div>)}</dl>
}
