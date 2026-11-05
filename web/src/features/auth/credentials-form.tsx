import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { Controller, useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { Link, useBeforeUnload, useBlocker, useLocation } from 'react-router'
import { Eye, EyeOff, LoaderCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldSet } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import * as api from '@/lib/api/auth'
import { messages } from '@/lib/messages'
import { locationReturnPath } from '@/lib/location-navigation'
import { AuthError } from './auth-error'
import { credentialSchema, emptyCredentials, type AuthMode, type CredentialValues } from './form-schema'
import type { AuthOperation } from './use-auth-action'

const control = 'min-h-12 h-auto whitespace-normal py-3 text-base! leading-normal'
const text = messages.auth

export function CredentialsForm({ mode, operation, username }: { mode: AuthMode; operation: AuthOperation; username?: string }) {
  const prefix = useId()
  const route = useLocation()
  const returnTo = locationReturnPath(route.state?.returnTo)
  const [visible, setVisible] = useState<Partial<Record<keyof CredentialValues, boolean>>>({})
  const errorRef = useRef<HTMLDivElement>(null)
  const form = useForm<CredentialValues>({ resolver: zodResolver(credentialSchema(mode)), defaultValues: { ...emptyCredentials }, mode: 'onBlur', reValidateMode: 'onBlur' })
  const { isDirty } = form.formState
  const { canLeave, allowNavigation, isPending } = operation
  const blocker = useBlocker(() => !canLeave() && (isDirty || isPending))
  useBeforeUnload(useCallback((event) => {
    if (!canLeave() && (isDirty || isPending)) {
      event.preventDefault()
      event.returnValue = ''
    }
  }, [isDirty, isPending, canLeave]))

  const { reset } = form
  useEffect(() => {
    const clear = () => {
      if (document.visibilityState === 'hidden') {
        reset({ ...emptyCredentials })
        setVisible({})
      }
    }
    document.addEventListener('visibilitychange', clear)
    return () => { document.removeEventListener('visibilitychange', clear) }
  }, [reset])

  useEffect(() => { if (operation.error) errorRef.current?.focus() }, [operation.error])

  const fields: { name: keyof CredentialValues; label: string; secret: boolean; autoComplete: string; help?: string }[] = [
    ...(mode === 'setup' ? [{ name: 'bootstrapToken' as const, label: text.bootstrap, secret: true, autoComplete: 'one-time-code', help: text.bootstrapHelp }] : []),
    ...(mode !== 'password' ? [{ name: 'username' as const, label: text.username, secret: false, autoComplete: 'username', help: text.usernameHelp }] : []),
    { name: 'password', label: mode === 'password' ? text.oldPassword : text.password, secret: true, autoComplete: mode === 'setup' ? 'new-password' : 'current-password' },
    ...(mode === 'password' ? [{ name: 'newPassword' as const, label: text.newPassword, secret: true, autoComplete: 'new-password', help: text.passwordHelp }] : []),
    ...(mode !== 'login' ? [{ name: 'confirmation' as const, label: text.confirmation, secret: true, autoComplete: 'new-password', help: mode === 'setup' ? text.passwordHelp : undefined }] : []),
  ]

  async function submit(values: CredentialValues) {
    try {
      await operation.run(mode, async (signal) => {
        if (mode === 'login') return { kind: 'login', admin: await api.login({ username: values.username, password: values.password }, signal) }
        if (mode === 'setup') {
          await api.setupAdmin({ username: values.username, password: values.password, bootstrap_token: values.bootstrapToken }, signal)
          return { kind: 'setup' }
        }
        const csrfToken = await api.fetchCSRF(signal)
        await api.changePassword({ old_password: values.password, new_password: values.newPassword }, { csrfToken, idempotencyKey: crypto.randomUUID(), signal })
        return { kind: 'password' }
      })
    } finally {
      form.reset({ ...emptyCredentials, username: values.username })
      setVisible({})
    }
  }

  const label = mode === 'login' ? text.login : mode === 'setup' ? text.setup : text.changePassword
  const pending = operation.pendingKind === 'logout' ? text.logoutPending : mode === 'login' ? text.loginPending : mode === 'setup' ? text.setupPending : text.passwordPending
  return (
    <>
      <form noValidate aria-busy={operation.isPending} onSubmit={form.handleSubmit(submit)} className="space-y-6">
        {username && <input type="hidden" name="username" autoComplete="username" value={username} readOnly />}
        {operation.error && <div ref={errorRef} tabIndex={-1} className="rounded-lg focus-visible:outline-2 focus-visible:outline-ring"><AuthError error={operation.error} setup={mode === 'setup'} /></div>}
        <FieldSet disabled={operation.isPending}>
          <FieldGroup>
            {fields.map((definition) => <Controller key={definition.name} name={definition.name} control={form.control} render={({ field, fieldState }) => {
              const id = `${prefix}-${definition.name}`
              return (
                <Field data-invalid={fieldState.invalid}>
                  <FieldLabel htmlFor={id} className="text-base leading-relaxed">{definition.label}<span aria-hidden="true">*</span></FieldLabel>
                  <div className={`grid min-w-0 gap-2 ${definition.secret ? '@min-[11rem]/field-group:grid-cols-[minmax(0,1fr)_auto]' : ''}`}>
                    <Input {...field} id={id} required aria-invalid={fieldState.invalid} aria-describedby={`${id}-help${fieldState.invalid ? ` ${id}-error` : ''}`}
                      type={definition.secret && !visible[definition.name] ? 'password' : 'text'}
                      autoComplete={definition.autoComplete} autoCapitalize="none" spellCheck={false}
                      maxLength={definition.name === 'username' ? 128 : 512} className="h-auto min-h-12 flex-1 py-3 text-base md:text-base" />
                    {definition.secret && <Button type="button" variant="outline" className="min-h-12 min-w-12 shrink-0 justify-self-end" aria-pressed={!!visible[definition.name]}
                      aria-label={`${visible[definition.name] ? text.hide : text.show}${definition.label}`}
                      onClick={() => setVisible((current) => ({ ...current, [definition.name]: !current[definition.name] }))}>
                      {visible[definition.name] ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
                    </Button>}
                  </div>
                  <FieldDescription id={`${id}-help`} className={definition.help ? 'text-sm leading-relaxed' : 'sr-only'}>{definition.help ?? definition.label}</FieldDescription>
                  <FieldError id={`${id}-error`} errors={[fieldState.error]} />
                </Field>
              )
            }} />)}
          </FieldGroup>
        </FieldSet>
        {mode === 'password' && <p className="text-base font-medium leading-relaxed">{text.passwordConsequence}</p>}
        <p className="text-sm leading-relaxed text-muted-foreground">{text.secretHelp}</p>
        {mode === 'login' && <p className="text-sm leading-relaxed text-muted-foreground">{text.recoveryHelp}</p>}
        <Button type="submit" disabled={operation.isPending} className={`${control} w-full gap-2`}>
          {operation.isPending && <LoaderCircle className="animate-spin motion-reduce:animate-none" aria-hidden="true" />}{operation.isPending ? pending : label}
        </Button>
        {mode !== 'password' && <Button variant="link" asChild className={`${control} w-full`}>
          <Link to={mode === 'login' ? '/setup' : '/login'} state={returnTo ? { returnTo } : undefined}>{mode === 'login' ? text.firstTime : text.alreadySetup}</Link>
        </Button>}
      </form>
      <AlertDialog open={blocker.state === 'blocked'} onOpenChange={(open) => { if (!open && !canLeave() && blocker.state === 'blocked') blocker.reset() }}>
        <AlertDialogContent className="max-h-[calc(100dvh-2rem)] w-[calc(100%_-_2rem)] max-w-lg animate-none! overflow-y-auto p-6">
          <AlertDialogHeader className="place-items-start text-left"><AlertDialogTitle className="text-xl">{text.leaveTitle}</AlertDialogTitle><AlertDialogDescription className="text-base leading-relaxed">{text.leaveHelp}</AlertDialogDescription></AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className={control}>{text.stay}</AlertDialogCancel>
            <AlertDialogAction className={control} onClick={() => { allowNavigation(); if (blocker.state === 'blocked') blocker.proceed() }}>{text.leave}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
