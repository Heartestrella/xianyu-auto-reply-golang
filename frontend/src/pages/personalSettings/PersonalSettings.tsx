import { useEffect, useState } from 'react'
import { Key, Save, User } from 'lucide-react'
import { changePassword, getUserSetting, updateUserSetting } from '@/api/settings'
import { ButtonLoading, PageLoading } from '@/components/common/Loading'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'

const CONTACT_WECHAT_KEY = 'contact_wechat'
const CONTACT_QQ_KEY = 'contact_qq'
const REDELIVERY_TRIGGER_KEYWORD_KEY = 'redelivery_trigger_keyword'

export function PersonalSettings() {
  const { user, clearAuth } = useAuthStore()
  const { addToast } = useUIStore()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [wechat, setWechat] = useState('')
  const [qq, setQQ] = useState('')
  const [keyword, setKeyword] = useState('')
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [changingPassword, setChangingPassword] = useState(false)

  useEffect(() => {
    Promise.all([getUserSetting(CONTACT_WECHAT_KEY), getUserSetting(CONTACT_QQ_KEY), getUserSetting(REDELIVERY_TRIGGER_KEYWORD_KEY)])
      .then(([wechatResult, qqResult, keywordResult]) => {
        setWechat(wechatResult.value || '')
        setQQ(qqResult.value || '')
        setKeyword(keywordResult.value || '')
      })
      .finally(() => setLoading(false))
  }, [])

  const save = async () => {
    setSaving(true)
    try {
      await Promise.all([
        updateUserSetting(CONTACT_WECHAT_KEY, wechat.trim(), '个人微信'),
        updateUserSetting(CONTACT_QQ_KEY, qq.trim(), '个人QQ'),
        updateUserSetting(REDELIVERY_TRIGGER_KEYWORD_KEY, keyword.trim(), '重发货触发关键字'),
      ])
      addToast({ type: 'success', message: '个人设置已保存' })
    } catch {
      addToast({ type: 'error', message: '保存失败' })
    } finally {
      setSaving(false)
    }
  }

  const updatePassword = async () => {
    if (newPassword.length < 6 || newPassword !== confirmPassword) {
      addToast({ type: 'warning', message: newPassword.length < 6 ? '新密码至少 6 位' : '两次输入的新密码不一致' })
      return
    }
    setChangingPassword(true)
    try {
      const result = await changePassword({ current_password: currentPassword, new_password: newPassword })
      if (!result.success) throw new Error(result.message)
      addToast({ type: 'success', message: '密码已修改，请重新登录' })
      clearAuth()
    } catch (error) {
      addToast({ type: 'error', message: error instanceof Error && error.message ? error.message : '密码修改失败' })
    } finally {
      setChangingPassword(false)
    }
  }

  if (loading) return <PageLoading />
  return (
    <div className="space-y-6">
      <div><h1 className="text-2xl font-semibold text-slate-900 dark:text-white">个人设置</h1><p className="mt-1 text-sm text-slate-500">当前本地用户：{user?.username || 'admin'}</p></div>
      <section className="vben-card">
        <div className="vben-card-header"><h2 className="vben-card-title"><User className="h-4 w-4" />业务信息</h2></div>
        <div className="vben-card-body grid grid-cols-1 gap-4 md:grid-cols-2">
          <label className="input-group"><span className="input-label">微信（可选）</span><input className="input-ios" value={wechat} onChange={(e) => setWechat(e.target.value)} /></label>
          <label className="input-group"><span className="input-label">QQ（可选）</span><input className="input-ios" value={qq} onChange={(e) => setQQ(e.target.value)} /></label>
          <label className="input-group md:col-span-2"><span className="input-label">补发货触发关键字</span><input className="input-ios" value={keyword} onChange={(e) => setKeyword(e.target.value)} placeholder="例如：补发" /></label>
          <div className="md:col-span-2"><button className="btn-ios-primary" onClick={save} disabled={saving}>{saving ? <ButtonLoading /> : <Save className="h-4 w-4" />}保存</button></div>
        </div>
      </section>
      <section className="vben-card">
        <div className="vben-card-header"><h2 className="vben-card-title"><Key className="h-4 w-4" />修改本地密码</h2></div>
        <div className="vben-card-body grid grid-cols-1 gap-4 md:grid-cols-2">
          <label className="input-group md:col-span-2"><span className="input-label">当前密码</span><input className="input-ios" type="password" value={currentPassword} onChange={(e) => setCurrentPassword(e.target.value)} /></label>
          <label className="input-group"><span className="input-label">新密码</span><input className="input-ios" type="password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} /></label>
          <label className="input-group"><span className="input-label">确认新密码</span><input className="input-ios" type="password" value={confirmPassword} onChange={(e) => setConfirmPassword(e.target.value)} /></label>
          <div className="md:col-span-2"><button className="btn-ios-primary" onClick={updatePassword} disabled={changingPassword}>{changingPassword ? <ButtonLoading /> : <Key className="h-4 w-4" />}修改密码</button></div>
        </div>
      </section>
    </div>
  )
}
