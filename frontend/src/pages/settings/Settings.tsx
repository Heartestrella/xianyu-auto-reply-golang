import { useEffect, useState } from 'react'
import { Globe, Save, Settings as SettingsIcon } from 'lucide-react'
import { Navigate } from 'react-router-dom'
import { getSystemSettings, updateSystemSettings } from '@/api/settings'
import { ButtonLoading, PageLoading } from '@/components/common/Loading'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import type { SystemSettings } from '@/types'
import { PasswordLoginModeSetting } from './PasswordLoginModeSetting'
import { ServiceRestartCard } from './ServiceRestartCard'
import { SliderModeSetting } from './SliderModeSetting'
import { TokenApiModeSetting } from './TokenApiModeSetting'

export function Settings() {
  const { user, isAuthenticated, token, _hasHydrated } = useAuthStore()
  const { addToast } = useUIStore()
  const [settings, setSettings] = useState<SystemSettings | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!_hasHydrated || !isAuthenticated || !token || !user?.is_admin) return
    getSystemSettings()
      .then((result) => { if (result.success && result.data) setSettings(result.data) })
      .catch(() => addToast({ type: 'error', message: '加载系统设置失败' }))
      .finally(() => setLoading(false))
  }, [_hasHydrated, isAuthenticated, token, user?.is_admin])

  if (_hasHydrated && (!isAuthenticated || !user?.is_admin)) return <Navigate to="/dashboard" replace />
  if (loading || !settings) return <PageLoading />

  const save = async () => {
    const days = Number(settings['log.retention_days'] || 7)
    if (!Number.isInteger(days) || days < 1 || days > 365) {
      addToast({ type: 'warning', message: '日志保留天数应为 1 到 365' })
      return
    }
    setSaving(true)
    try {
      const result = await updateSystemSettings({
        'log.retention_days': String(days),
        'account.face_verify_timeout_disable': Boolean(settings['account.face_verify_timeout_disable'] ?? true),
        'proxy.enabled': Boolean(settings['proxy.enabled'] ?? false),
        'proxy.api_url': String(settings['proxy.api_url'] || '').trim(),
      })
      addToast({ type: result.success ? 'success' : 'error', message: result.message || (result.success ? '设置已保存' : '保存失败') })
    } catch {
      addToast({ type: 'error', message: '保存失败' })
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-4">
      <div className="page-header flex items-center justify-between gap-4">
        <div><h1 className="page-title">系统设置</h1><p className="page-description">仅保留个人运行所需配置</p></div>
        <button onClick={save} disabled={saving} className="btn-ios-primary">{saving ? <ButtonLoading /> : <Save className="h-4 w-4" />}保存设置</button>
      </div>
      <ServiceRestartCard />
      <div className="vben-card">
        <div className="vben-card-header"><h2 className="vben-card-title"><SettingsIcon className="h-4 w-4" />本地运行</h2></div>
        <div className="vben-card-body space-y-4">
          <div className="flex items-center justify-between gap-4 border-b border-slate-100 py-3 dark:border-slate-700">
            <div><p className="font-medium text-slate-900 dark:text-slate-100">日志保留天数</p><p className="text-sm text-slate-500">自动清理过期运行日志（1～365 天）</p></div>
            <input type="number" min={1} max={365} className="input-ios w-24 text-center" value={String(settings['log.retention_days'] || '7')} onChange={(e) => setSettings({ ...settings, 'log.retention_days': e.target.value })} />
          </div>
          <div className="flex items-center justify-between gap-4 py-3">
            <div><p className="font-medium text-slate-900 dark:text-slate-100">人脸验证超时自动停用账号</p><p className="text-sm text-slate-500">避免异常账号持续发起请求</p></div>
            <label className="switch-ios"><input type="checkbox" checked={Boolean(settings['account.face_verify_timeout_disable'] ?? true)} onChange={(e) => setSettings({ ...settings, 'account.face_verify_timeout_disable': e.target.checked })} /><span className="switch-slider" /></label>
          </div>
          <PasswordLoginModeSetting value={settings['password_login.mode']} remoteUrl={settings['password_login.remote_url']} remoteSecretKey={settings['password_login.remote_secret_key']} onSaved={(mode, url, secret) => setSettings({ ...settings, 'password_login.mode': mode, 'password_login.remote_url': url || '', 'password_login.remote_secret_key': secret || '' })} />
          <SliderModeSetting value={settings['captcha.slider_mode']} onSaved={(mode) => setSettings({ ...settings, 'captcha.slider_mode': mode })} />
          <TokenApiModeSetting value={settings['token.api_mode']} remoteUrl={settings['token.remote_url']} remoteSecretKey={settings['token.remote_secret_key']} onSaved={(mode, url, secret) => setSettings({ ...settings, 'token.api_mode': mode, 'token.remote_url': url || '', 'token.remote_secret_key': secret || '' })} />
        </div>
      </div>
      <div className="vben-card">
        <div className="vben-card-header"><h2 className="vben-card-title"><Globe className="h-4 w-4" />网络代理</h2></div>
        <div className="vben-card-body space-y-4">
          <div className="flex items-center justify-between gap-4"><div><p className="font-medium text-slate-900 dark:text-slate-100">启用代理 API</p><p className="text-sm text-slate-500">仅在你确实配置了代理服务时开启</p></div><label className="switch-ios"><input type="checkbox" checked={Boolean(settings['proxy.enabled'] ?? false)} onChange={(e) => setSettings({ ...settings, 'proxy.enabled': e.target.checked })} /><span className="switch-slider" /></label></div>
          <label className="input-group"><span className="input-label">代理 API 地址</span><textarea className="input-ios min-h-[72px] resize-y" value={String(settings['proxy.api_url'] || '')} onChange={(e) => setSettings({ ...settings, 'proxy.api_url': e.target.value })} placeholder="https://example.com/proxy" /></label>
        </div>
      </div>
    </div>
  )
}
