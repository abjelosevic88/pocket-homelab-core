import React, { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { api, V1 } from './api'
import type { AuthStatus, User } from './types'

interface AppState {
  status: AuthStatus | null
  user: User | null
  theme: 'light' | 'dark'
  toggleTheme: () => void
  refresh: () => Promise<void>
  setUser: (u: User | null) => void
}
const Ctx = createContext<AppState>(null!)
export function useApp() { return useContext(Ctx) }

export function AppProvider({ children }: { children: React.ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [theme, setTheme] = useState<'light' | 'dark'>(() => (localStorage.getItem('pc-theme') as 'light' | 'dark') || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'))
  useEffect(() => { document.documentElement.dataset.theme = theme; localStorage.setItem('pc-theme', theme) }, [theme])
  useEffect(() => { if (status?.title) document.title = status.title }, [status?.title])
  const refresh = useCallback(async () => {
    const st = await api.get<AuthStatus>(`${V1}/auth/status`)
    setStatus(st)
    setUser(st.user)
  }, [])
  useEffect(() => { refresh().catch(() => setStatus({ needs_setup: false, auth_mode: 'local', authenticated: false, user: null, version: '', title: 'Pocket Core', box_name: 'Pocket Box' })) }, [refresh])
  useEffect(() => {
    const h = () => { setUser(null); setStatus(s => s ? { ...s, authenticated: false, user: null } : s) }
    window.addEventListener('pc:unauthorized', h)
    return () => window.removeEventListener('pc:unauthorized', h)
  }, [])
  return <Ctx.Provider value={{ status, user, theme, toggleTheme: () => setTheme(t => t === 'dark' ? 'light' : 'dark'), refresh, setUser }}>{children}</Ctx.Provider>
}
