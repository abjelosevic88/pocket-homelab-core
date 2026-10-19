export interface User { id: number; email: string; name: string; role: 'admin' | 'member'; disabled: boolean; created_at: string; updated_at: string }
export interface Principal { kind: 'user' | 'app' | 'device'; user?: User | null; app?: App | null; device?: Device | null; scope?: string }
export interface AuthStatus { needs_setup: boolean; auth_mode: string; authenticated: boolean; user: User | null; version: string; title: string; box_name: string; sso?: boolean; sso_name?: string; password_login?: boolean }
export interface Settings { title: string; box_name: string; registration_key: string; setup_complete: boolean }
export interface App {
  id: number; slug: string; name: string; description: string; icon: string; version: string; url: string; public_url: string
  health_path: string; env_prefix: string; manifest: Record<string, unknown>; token_prefix: string
  status: 'ok' | 'down' | 'unknown'; last_seen_at?: string; last_check_at?: string; last_error: string; created_at: string; updated_at: string
}
export interface CatalogField { key: string; label: string; secret: boolean; help?: string }
export interface CatalogEntry { type: string; name: string; prefix: string; group: string; url_key: string; pocket: boolean; fields: CatalogField[] }
export interface Service {
  id: number; type: string; name: string; url: string; public_url: string; fields: Record<string, string>; app_id?: number
  enabled: boolean; notes: string; created_at: string; updated_at: string; catalog: CatalogEntry
  audience: 'all' | 'admins' | 'users'; audience_users: number[]
}
export interface Credential { service: Service; url: string; fields: Record<string, string>; set: boolean; visible: boolean }
export interface Device { id: number; user_id: number; name: string; platform: string; token_prefix: string; last_seen_at?: string; last_ip: string; created_at: string }
export interface Event { id: number; topic: string; source: string; subject: string; payload: unknown; created_at: string }
export interface Topic { topic: string; count: number; last: string }
export interface Webhook { id: number; app_id?: number; name: string; url: string; topics: string; has_secret: boolean; enabled: boolean; created_at: string }
export interface Delivery { id: number; webhook_id: number; event_id: number; status: string; attempts: number; next_at: string; last_code: number; last_error: string; delivered_at?: string; created_at: string }
export interface OIDCClient { id: number; client_id: string; has_secret: boolean; name: string; redirect_uris: string[]; app_id?: number; created_at: string }
export interface APIToken { id: number; user_id: number; name: string; prefix: string; last_used_at?: string; created_at: string }
export interface Pairing { code: string; expires_at: string; url: string; qr_png: string; qr_svg: string; core_url: string; ttl_s: number }
export interface Overview {
  box_name: string; version: string; apps: App[]; apps_up: number; services_total: number; services_enabled: number
  devices: Device[]; recent_events: Event[]; deliveries: Record<string, number>; listeners: number; member?: boolean
}
export interface MyService { id: number; type: string; name: string; group: string; url: string; icon: string; pocket: boolean }
export interface SystemInfo { version: string; go: string; os: string; uptime_s: number; started_at: string; base_url: string; issuer: string; auth_mode: string; data_dir: string; counts: Record<string, number>; listeners: number; box_name: string; health_interval_s: number; event_retention_h: number }
