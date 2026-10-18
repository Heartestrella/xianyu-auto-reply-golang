import { useEffect, useState } from 'react'
import { ChevronDown, LogOut, Moon, Sun, UserCog } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { useAuthStore } from '@/store/authStore'
import { cn } from '@/utils/cn'
import { initializeThemeMode, toggleThemeMode } from '@/utils/theme'

export function TopNavbar({ systemName = '闲鱼自动回复' }: { systemName?: string }) {
  const navigate = useNavigate()
  const { user, clearAuth } = useAuthStore()
  const [isDark, setIsDark] = useState(false)
  const [showUserMenu, setShowUserMenu] = useState(false)

  useEffect(() => setIsDark(initializeThemeMode() === 'dark'), [])
  const toggleTheme = () => setIsDark(toggleThemeMode() === 'dark')
  const logout = () => { clearAuth(); navigate('/login') }

  return (
    <div className="top-navbar">
      <div className="ml-12 min-w-0 flex-1 sm:ml-0">
        <span className="hidden truncate text-sm text-slate-500 dark:text-slate-400 sm:inline">{systemName} · 个人自用版</span>
        <span className="truncate text-sm text-slate-500 dark:text-slate-400 sm:hidden">个人自用版</span>
      </div>
      <div className="flex items-center gap-1 sm:gap-2">
        <button onClick={toggleTheme} className="rounded-md p-2 text-slate-500 transition-colors hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-700" title={isDark ? '切换到亮色模式' : '切换到暗色模式'}>{isDark ? <Sun className="h-5 w-5" /> : <Moon className="h-5 w-5" />}</button>
        <div className="relative">
          <button onClick={() => setShowUserMenu(!showUserMenu)} className="flex items-center gap-2 rounded-md px-2 py-1.5 text-slate-700 transition-colors hover:bg-slate-100 dark:text-slate-200 dark:hover:bg-slate-700">
            <div className="flex h-7 w-7 items-center justify-center rounded-full bg-blue-500 text-xs font-medium text-white">{(user?.username || 'U').charAt(0).toUpperCase()}</div>
            <span className="hidden text-sm font-medium sm:inline">{user?.username || '本地用户'}</span><ChevronDown className="hidden h-4 w-4 text-slate-400 sm:block" />
          </button>
          {showUserMenu && <><div className="fixed inset-0 z-40" onClick={() => setShowUserMenu(false)} /><div className="absolute right-0 z-50 mt-2 w-48 rounded-lg bg-white py-1 shadow-lg ring-1 ring-black/5 dark:bg-slate-800 dark:ring-white/10">
            <div className="border-b border-slate-100 px-4 py-2 dark:border-slate-700"><p className="text-sm font-medium text-slate-900 dark:text-slate-100">{user?.username}</p><p className="text-xs text-slate-500">本地管理员</p></div>
            <button onClick={() => { setShowUserMenu(false); navigate('/personal-settings') }} className={cn('flex w-full items-center gap-2 px-4 py-2 text-sm text-slate-700 hover:bg-slate-50 dark:text-slate-300 dark:hover:bg-slate-700')}><UserCog className="h-4 w-4" />个人设置</button>
            <button onClick={logout} className="flex w-full items-center gap-2 px-4 py-2 text-sm text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/20"><LogOut className="h-4 w-4" />退出登录</button>
          </div></>}
        </div>
      </div>
    </div>
  )
}
