import { FormEvent, useState } from 'react'
import { LockKeyhole, User } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { login } from '@/api/auth'
import { ButtonLoading } from '@/components/common/Loading'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'

export function Login() {
  const navigate = useNavigate()
  const { setAuth } = useAuthStore()
  const { addToast } = useUIStore()
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!username.trim() || !password) {
      addToast({ type: 'warning', message: '请输入用户名和密码' })
      return
    }
    setLoading(true)
    try {
      const result = await login({ username: username.trim(), password })
      if (!result.success || !result.token || !result.refresh_token || !result.user_id) {
        addToast({ type: 'error', message: result.message || '登录失败' })
        return
      }
      setAuth(result.token, result.refresh_token, {
        user_id: result.user_id,
        username: result.username || username.trim(),
        is_admin: true,
        account_limit: result.account_limit,
      })
      navigate('/dashboard', { replace: true })
    } catch {
      addToast({ type: 'error', message: '登录失败，请检查本地服务是否已启动' })
    } finally {
      setLoading(false)
    }
  }

  return (
    <main className="min-h-screen bg-slate-950 px-4 py-12 text-slate-100 flex items-center justify-center">
      <section className="w-full max-w-md rounded-2xl border border-slate-800 bg-slate-900 p-8 shadow-2xl">
        <div className="mb-8">
          <p className="text-sm font-medium text-blue-400">个人自用版</p>
          <h1 className="mt-2 text-2xl font-semibold">闲鱼自动回复</h1>
          <p className="mt-2 text-sm text-slate-400">数据仅保存在本机。请使用本地管理员账号登录。</p>
        </div>
        <form className="space-y-5" onSubmit={submit}>
          <label className="block">
            <span className="mb-2 block text-sm text-slate-300">用户名</span>
            <span className="relative block"><User className="absolute left-3 top-3 h-5 w-5 text-slate-500" /><input className="w-full rounded-xl border border-slate-700 bg-slate-950 py-3 pl-11 pr-3 outline-none focus:border-blue-500" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" /></span>
          </label>
          <label className="block">
            <span className="mb-2 block text-sm text-slate-300">密码</span>
            <span className="relative block"><LockKeyhole className="absolute left-3 top-3 h-5 w-5 text-slate-500" /><input className="w-full rounded-xl border border-slate-700 bg-slate-950 py-3 pl-11 pr-3 outline-none focus:border-blue-500" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" autoFocus /></span>
          </label>
          <button className="btn-ios-primary w-full justify-center" type="submit" disabled={loading}>{loading && <ButtonLoading />}登录</button>
        </form>
        <p className="mt-6 text-xs leading-5 text-slate-500">首次启动默认账号为 admin / admin123，登录后请立即在个人设置中修改密码。</p>
      </section>
    </main>
  )
}
