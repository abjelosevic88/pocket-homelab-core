import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import './styles/base.css'
import './styles/core.css'
import './styles/app.css'
import './styles/settings.css'
import { AppProvider, useApp } from './lib/app-context'
import { Loading, ToastProvider } from './components/ui'
import Layout from './components/Layout'
import { ErrorBoundary } from './components/ErrorBoundary'
import Login from './pages/Login'
import Home from './pages/Home'
import Apps from './pages/Apps'
import Services from './pages/Services'
import Events from './pages/Events'
import Devices from './pages/Devices'
import Users from './pages/Users'
import Settings from './pages/Settings'
import Links from './pages/Links'
import AIConnections from './pages/AIConnections'
import AIPairing from './pages/AIPairing'

function Gate({ children }: { children: React.ReactNode }) {
  const { status, user } = useApp()
  const loc = useLocation()
  if (!status) return <div className="boot"><img src="/favicon.svg" alt="" /><Loading /></div>
  if (status.needs_setup) return <Navigate to="/setup" replace />
  if (!user) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />
  return <>{children}</>
}

function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login mode="login" />} />
      <Route path="/setup" element={<Login mode="setup" />} />
      <Route path="/connect" element={<Navigate to="/ai-connections" replace />} />
      <Route path="/settings/:tab?" element={<Gate><ErrorBoundary where="settings"><Settings /></ErrorBoundary></Gate>} />
      <Route element={<Gate><ErrorBoundary where="core"><Layout /></ErrorBoundary></Gate>}>
        <Route path="/" element={<Home />} />
        <Route path="/apps" element={<Apps />} />
        <Route path="/apps/:slug" element={<Apps />} />
        <Route path="/services" element={<Services />} />
        <Route path="/events" element={<Events />} />
        <Route path="/links" element={<Links />} />
        <Route path="/devices" element={<Devices />} />
        <Route path="/users" element={<Users />} />
        <Route path="/ai-connections" element={<AIConnections />} />
        <Route path="/ai-connections/pair" element={<AIPairing />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  )
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ErrorBoundary where="app"><ToastProvider><AppProvider><App /></AppProvider></ToastProvider></ErrorBoundary>
    </BrowserRouter>
  </React.StrictMode>,
)
