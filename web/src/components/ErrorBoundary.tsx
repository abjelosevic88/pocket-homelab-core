import React from 'react'

// A crash in one screen shows what went wrong instead of leaving the whole page blank.
export class ErrorBoundary extends React.Component<{ children: React.ReactNode; where?: string }, { error: Error | null }> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) { return { error } }
  componentDidCatch(error: Error, info: React.ErrorInfo) { console.error(this.props.where || 'app', error, info?.componentStack) }
  render() {
    if (!this.state.error) return this.props.children
    const translated = /removeChild|insertBefore|not a child of this node/i.test(this.state.error.message)
    return (
      <div className="crash">
        <h2>Something went wrong here</h2>
        {translated && <p>The browser translated this page, which pulls the text out from under it. Turn translation off for this site (the translate icon in the address bar → “Never translate this site”).</p>}
        <pre className="mono small">{this.state.error.message}</pre>
        <div className="row"><button type="button" className="btn primary" onClick={() => location.reload()}>Reload</button><button type="button" className="btn" onClick={() => this.setState({ error: null })}>Try again</button></div>
      </div>
    )
  }
}
