import { Component, type ReactNode } from 'react'
import { Link } from 'react-router'
import { Button } from '@/components/ui/button'
import { messages } from '@/lib/messages'

// Failed lazy chunks remain rejected until an explicit reload. Never force a
// refresh that could discard an active form or silently replay an operation.
export class AppErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false }

  static getDerivedStateFromError() { return { failed: true } }

  render() {
    if (!this.state.failed) return this.props.children
    return (
      <section role="alert" className="space-y-6 py-12">
        <h1 className="text-3xl font-semibold">{messages.app.loadError}</h1>
        <p className="max-w-prose text-muted-foreground">{messages.app.loadErrorHelp}</p>
        <div className="flex flex-wrap gap-3">
          <Button className="h-auto min-h-12 whitespace-normal py-3 text-base" onClick={() => window.location.reload()}>{messages.app.reload}</Button>
          <Button variant="outline" asChild className="h-auto min-h-12 whitespace-normal py-3 text-base"><Link to="/">{messages.app.returnHome}</Link></Button>
        </div>
      </section>
    )
  }
}
