import React, { Suspense, useEffect, useState, useRef } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { useAuthStore } from '@/store/authStore'
import { MainLayout } from '@/components/layout/MainLayout'
import { Toast } from '@/components/common/Toast'
import { verifyToken } from '@/api/auth'
import { getHiddenMenuKeysFromSettings, getPublicSystemSettings, getSystemSettings } from '@/api/settings'
import { getMenuAccessFallbackPath, isPathBlockedForUser } from '@/config/navigation'
import { useMenuVisibilityStore } from '@/store/menuVisibilityStore'
import { applyThemeSettings, initializeThemeMode } from '@/utils/theme'

// 个人版仅保留本地账号登录。
import { Login } from '@/pages/auth/Login'

// 页面组件懒加载，按需加载提升首屏速度
const Dashboard = React.lazy(() => import('@/pages/dashboard/Dashboard').then(m => ({ default: m.Dashboard })))
const DataOverview = React.lazy(() => import('@/pages/data_analysis/DataOverview').then(m => ({ default: m.DataOverview })))
const Accounts = React.lazy(() => import('@/pages/accounts/Accounts').then(m => ({ default: m.Accounts })))
const Items = React.lazy(() => import('@/pages/items/Items').then(m => ({ default: m.Items })))
const Orders = React.lazy(() => import('@/pages/orders/Orders').then(m => ({ default: m.Orders })))
const Keywords = React.lazy(() => import('@/pages/keywords/Keywords').then(m => ({ default: m.Keywords })))
const NotificationChannels = React.lazy(() => import('@/pages/notifications/NotificationChannels').then(m => ({ default: m.NotificationChannels })))
const MessageNotifications = React.lazy(() => import('@/pages/notifications/MessageNotifications').then(m => ({ default: m.MessageNotifications })))
const Settings = React.lazy(() => import('@/pages/settings/Settings').then(m => ({ default: m.Settings })))
const MessageFilters = React.lazy(() => import('@/pages/messageFilters/MessageFilters').then(m => ({ default: m.MessageFilters })))
const ItemSearch = React.lazy(() => import('@/pages/search/ItemSearch').then(m => ({ default: m.ItemSearch })))
const GoofishCompass = React.lazy(() => import('@/pages/compass/GoofishCompass').then(m => ({ default: m.GoofishCompass })))
const GoofishScheduledCrawler = React.lazy(() => import('@/pages/crawler/GoofishScheduledCrawler').then(m => ({ default: m.GoofishScheduledCrawler })))
const Cards = React.lazy(() => import('@/pages/cards/Cards').then(m => ({ default: m.Cards })))
const PersonalSettings = React.lazy(() => import('@/pages/personalSettings/PersonalSettings').then(m => ({ default: m.PersonalSettings })))
const Blacklist = React.lazy(() => import('@/pages/blacklist/Blacklist'))


// 同意后发货 - 买家提货页（无需登录，公开访问）
const AgreePickupPage = React.lazy(() => import('@/pages/agree-pickup/AgreePickupPage').then(m => ({ default: m.AgreePickupPage })))

// 商品发布页面懒加载
const ProductMaterials = React.lazy(() => import('@/pages/product-publish/ProductMaterials').then(m => ({ default: m.ProductMaterials })))
const ProductPublish = React.lazy(() => import('@/pages/product-publish/ProductPublish').then(m => ({ default: m.ProductPublish })))
const BatchPublish = React.lazy(() => import('@/pages/product-publish/BatchPublish').then(m => ({ default: m.BatchPublish })))
const PublishAddresses = React.lazy(() => import('@/pages/product-publish/PublishAddresses').then(m => ({ default: m.PublishAddresses })))
const PublishLogs = React.lazy(() => import('@/pages/product-publish/PublishLogs').then(m => ({ default: m.PublishLogs })))
const ListingMonitor = React.lazy(() => import('@/pages/product-monitor/ListingMonitor').then(m => ({ default: m.ListingMonitor })))
const MonitorOverview = React.lazy(() => import('@/pages/product-monitor/MonitorOverview').then(m => ({ default: m.MonitorOverview })))
const MonitorCategory = React.lazy(() => import('@/pages/product-monitor/MonitorCategory').then(m => ({ default: m.MonitorCategory })))
const MonitorLogs = React.lazy(() => import('@/pages/product-monitor/MonitorLogs').then(m => ({ default: m.MonitorLogs })))
const MonitorItems = React.lazy(() => import('@/pages/product-monitor/MonitorItems').then(m => ({ default: m.MonitorItems })))
const OrderFallbackAccount = React.lazy(() => import('@/pages/product-monitor/OrderFallbackAccount').then(m => ({ default: m.OrderFallbackAccount })))
const CollectFallbackAccount = React.lazy(() => import('@/pages/product-monitor/CollectFallbackAccount').then(m => ({ default: m.CollectFallbackAccount })))

// 管理员页面懒加载
const Logs = React.lazy(() => import('@/pages/admin/Logs').then(m => ({ default: m.Logs })))
const AutoReplyLogs = React.lazy(() => import('@/pages/autoReplyLogs/AutoReplyLogs').then(m => ({ default: m.AutoReplyLogs })))
const RiskLogs = React.lazy(() => import('@/pages/admin/RiskLogs').then(m => ({ default: m.RiskLogs })))
const AccountLoginLogs = React.lazy(() => import('@/pages/admin/AccountLoginLogs').then(m => ({ default: m.AccountLoginLogs })))
const DbBackupLogs = React.lazy(() => import('@/pages/admin/DbBackupLogs').then(m => ({ default: m.DbBackupLogs })))
const DataManagement = React.lazy(() => import('@/pages/admin/DataManagement').then(m => ({ default: m.DataManagement })))
const ScheduledTasks = React.lazy(() => import('@/pages/admin/ScheduledTasks').then(m => ({ default: m.ScheduledTasks })))
const RedeliveryBatches = React.lazy(() => import('@/pages/redeliveryLogs/RedeliveryBatches').then(m => ({ default: m.RedeliveryBatches })))
const RedeliveryBatchDetailPage = React.lazy(() => import('@/pages/redeliveryLogs/RedeliveryBatchDetail').then(m => ({ default: m.RedeliveryBatchDetailPage })))
const RateBatches = React.lazy(() => import('@/pages/rateLogs/RateBatches').then(m => ({ default: m.RateBatches })))
const RateBatchDetailPage = React.lazy(() => import('@/pages/rateLogs/RateBatchDetail').then(m => ({ default: m.RateBatchDetailPage })))
const PolishBatches = React.lazy(() => import('@/pages/polishLogs/PolishBatches').then(m => ({ default: m.PolishBatches })))
const PolishBatchDetailPage = React.lazy(() => import('@/pages/polishLogs/PolishBatchDetail').then(m => ({ default: m.PolishBatchDetailPage })))
const LoginRenewBatches = React.lazy(() => import('@/pages/loginRenewLogs/LoginRenewBatches').then(m => ({ default: m.LoginRenewBatches })))
const LoginRenewBatchDetailPage = React.lazy(() => import('@/pages/loginRenewLogs/LoginRenewBatchDetail').then(m => ({ default: m.LoginRenewBatchDetailPage })))
const TokenRenewalBatches = React.lazy(() => import('@/pages/tokenRenewalLogs/TokenRenewalBatches').then(m => ({ default: m.TokenRenewalBatches })))
const TokenRenewalBatchDetailPage = React.lazy(() => import('@/pages/tokenRenewalLogs/TokenRenewalBatchDetail').then(m => ({ default: m.TokenRenewalBatchDetailPage })))
const CookiesRefreshBatches = React.lazy(() => import('@/pages/cookiesRefreshLogs/CookiesRefreshBatches').then(m => ({ default: m.CookiesRefreshBatches })))
const CookiesRefreshBatchDetailPage = React.lazy(() => import('@/pages/cookiesRefreshLogs/CookiesRefreshBatchDetail').then(m => ({ default: m.CookiesRefreshBatchDetailPage })))
const ApiCookieRenewBatches = React.lazy(() => import('@/pages/apiCookieRenewLogs/ApiCookieRenewBatches').then(m => ({ default: m.ApiCookieRenewBatches })))
const ApiCookieRenewBatchDetailPage = React.lazy(() => import('@/pages/apiCookieRenewLogs/ApiCookieRenewBatchDetail').then(m => ({ default: m.ApiCookieRenewBatchDetailPage })))
const CloseNoticeBatches = React.lazy(() => import('@/pages/closeNoticeLogs/CloseNoticeBatches').then(m => ({ default: m.CloseNoticeBatches })))
const CloseNoticeBatchDetailPage = React.lazy(() => import('@/pages/closeNoticeLogs/CloseNoticeBatchDetail').then(m => ({ default: m.CloseNoticeBatchDetailPage })))
const RedFlowerBatches = React.lazy(() => import('@/pages/redFlowerLogs/RedFlowerBatches').then(m => ({ default: m.RedFlowerBatches })))
const RedFlowerBatchDetailPage = React.lazy(() => import('@/pages/redFlowerLogs/RedFlowerBatchDetail').then(m => ({ default: m.RedFlowerBatchDetailPage })))

// 懒加载页面的加载遮罩
function PageLoading() {
  return (
    <div className="flex items-center justify-center h-full min-h-[200px]">
      <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-blue-500"></div>
    </div>
  )
}

// Protected route wrapper
function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { user, isAuthenticated, setAuth, clearAuth, token: storeToken, _hasHydrated } = useAuthStore()
  const { hiddenMenuKeys, isExeMode, setHiddenMenuKeys, setIsExeMode } = useMenuVisibilityStore()
  const location = useLocation()
  const [authState, setAuthState] = useState<'checking' | 'authenticated' | 'unauthenticated'>('checking')
  const [menuState, setMenuState] = useState<'checking' | 'ready'>('checking')
  const checkingRef = useRef(false)

  useEffect(() => {
    // 等待 zustand persist 完成 hydration
    if (!_hasHydrated) {
      return
    }
    
    // 防止并发检查
    if (checkingRef.current) {
      return
    }
    
    const checkAuth = async () => {
      checkingRef.current = true
      
      // 优先使用 store 中的 token，其次是 localStorage
      const token = storeToken || localStorage.getItem('auth_token')
      
      if (!token) {
        setAuthState('unauthenticated')
        setMenuState('ready')
        checkingRef.current = false
        return
      }

      // 验证 token 有效性（不再单纯相信本地 isAuthenticated 状态）
      try {
        const result = await verifyToken()
        if (result.authenticated && result.user_id) {
          const refreshToken = localStorage.getItem('refresh_token') || ''
          setAuth(token, refreshToken, {
            user_id: result.user_id,
            username: result.username || '',
            is_admin: result.is_admin || false,
            account_limit: result.account_limit,
          })
          setAuthState('authenticated')
          
          try {
            const settingsResult = await getSystemSettings()
            if (settingsResult.success && settingsResult.data) {
              applyThemeSettings(settingsResult.data)
              setIsExeMode(Boolean(settingsResult.data['runtime.is_exe_mode']))
              setHiddenMenuKeys(getHiddenMenuKeysFromSettings(settingsResult.data))
            } else {
              setIsExeMode(false)
              setHiddenMenuKeys([])
            }
          } catch {
            setIsExeMode(false)
            setHiddenMenuKeys([])
          } finally {
            setMenuState('ready')
          }
        } else {
          clearAuth()
          setAuthState('unauthenticated')
          setMenuState('ready')
        }
      } catch {
        clearAuth()
        setAuthState('unauthenticated')
        setMenuState('ready')
      } finally {
        checkingRef.current = false
      }
    }

    checkAuth()
  }, [_hasHydrated, isAuthenticated, storeToken, setAuth, clearAuth, setHiddenMenuKeys, setIsExeMode])

  // 等待 hydration 或检查完成
  if (!_hasHydrated || authState === 'checking' || menuState === 'checking') {
    return (
      <div className="min-h-screen flex items-center justify-center bg-slate-50 dark:bg-slate-900">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-blue-500"></div>
      </div>
    )
  }

  if (authState === 'unauthenticated') {
    return <Navigate to="/login" replace />
  }

  if (isPathBlockedForUser(location.pathname, hiddenMenuKeys, Boolean(user?.is_admin), isExeMode)) {
    return <Navigate to={getMenuAccessFallbackPath(hiddenMenuKeys, Boolean(user?.is_admin), isExeMode)} replace />
  }

  return <>{children}</>
}

function App() {
  const { setIsExeMode } = useMenuVisibilityStore()

  useEffect(() => {
    let cancelled = false

    initializeThemeMode()
    applyThemeSettings()

    const bootstrapTheme = async () => {
      try {
        const result = await getPublicSystemSettings()
        if (!cancelled && result.success) {
          applyThemeSettings(result.data)
          setIsExeMode(Boolean(result.data?.['runtime.is_exe_mode']))
        }
      } catch {
        if (!cancelled) {
          applyThemeSettings()
          setIsExeMode(false)
        }
      }
    }

    bootstrapTheme()

    return () => {
      cancelled = true
    }
  }, [setIsExeMode])

  return (
    <BrowserRouter>
      <Toast />
      <Suspense fallback={<PageLoading />}>
        <Routes>
          {/* Public routes */}
          <Route path="/login" element={<Login />} />
          {/* 同意后发货提货页：无需登录，公开访问 */}
          <Route path="/agree-pickup" element={<AgreePickupPage />} />

          {/* Protected routes */}
          <Route
            path="/"
            element={
              <ProtectedRoute>
                <MainLayout />
              </ProtectedRoute>
            }
          >
            <Route index element={<Navigate to="/dashboard" replace />} />
            <Route path="dashboard" element={<Dashboard />} />
            <Route path="data-analysis/overview" element={<DataOverview />} />
            <Route path="accounts" element={<Accounts />} />
            <Route path="items" element={<Items />} />
            <Route path="orders" element={<Orders />} />
            <Route path="keywords" element={<Keywords />} />
            <Route path="message-logs" element={<AutoReplyLogs />} />
            <Route path="account-login-logs" element={<Navigate to="/admin/account-login-logs" replace />} />
            <Route path="risk-logs" element={<RiskLogs />} />
            <Route path="message-filters" element={<MessageFilters />} />
            {/* 在线聊天由 MainLayout 直接渲染以实现 KeepAlive，此处仅保留路由占位 */}
            <Route path="online-chat-new" element={<></>} />
            <Route path="notification-channels" element={<NotificationChannels />} />
            <Route path="message-notifications" element={<MessageNotifications />} />
            <Route path="item-search" element={<ItemSearch />} />
            <Route path="goofish-compass" element={<GoofishCompass />} />
            <Route path="goofish-scheduled-crawler" element={<GoofishScheduledCrawler />} />
            <Route path="cards" element={<Cards />} />
            {/* 商品发布 */}
            <Route path="product-publish/materials" element={<ProductMaterials />} />
            <Route path="product-publish/single" element={<ProductPublish />} />
            <Route path="product-publish/batch" element={<BatchPublish />} />
            <Route path="product-publish/addresses" element={<PublishAddresses />} />
            <Route path="product-publish/logs" element={<PublishLogs />} />
            {/* 商品监控 */}
            <Route path="product-monitor/overview" element={<MonitorOverview />} />
            <Route path="product-monitor/categories" element={<MonitorCategory />} />
            <Route path="product-monitor/listing" element={<ListingMonitor />} />
            <Route path="product-monitor/logs" element={<MonitorLogs />} />
            <Route path="product-monitor/items" element={<MonitorItems />} />
            <Route path="product-monitor/order-fallback-accounts" element={<OrderFallbackAccount />} />
            <Route path="product-monitor/collect-fallback-accounts" element={<CollectFallbackAccount />} />
            <Route path="personal-settings" element={<PersonalSettings />} />
            <Route path="blacklist" element={<Blacklist />} />
            <Route path="settings" element={<Settings />} />

            {/* Admin routes */}
            <Route path="admin/logs" element={<Logs />} />
            <Route path="admin/account-login-logs" element={<AccountLoginLogs />} />
            <Route path="admin/db-backup-logs" element={<DbBackupLogs />} />
            <Route path="admin/auto-reply-logs" element={<Navigate to="/message-logs" replace />} />
            <Route path="admin/risk-logs" element={<Navigate to="/risk-logs" replace />} />
            <Route path="admin/data" element={<DataManagement />} />
            <Route path="admin/redelivery-batches" element={<RedeliveryBatches />} />
            <Route path="admin/redelivery-batches/:batchId" element={<RedeliveryBatchDetailPage />} />
            <Route path="admin/rate-batches" element={<RateBatches />} />
            <Route path="admin/rate-batches/:batchId" element={<RateBatchDetailPage />} />
            <Route path="admin/polish-batches" element={<PolishBatches />} />
            <Route path="admin/polish-batches/:batchId" element={<PolishBatchDetailPage />} />
            <Route path="admin/login-renew-batches" element={<LoginRenewBatches />} />
            <Route path="admin/login-renew-batches/:batchId" element={<LoginRenewBatchDetailPage />} />
            <Route path="admin/token-renewal-batches" element={<TokenRenewalBatches />} />
            <Route path="admin/token-renewal-batches/:batchId" element={<TokenRenewalBatchDetailPage />} />
            <Route path="admin/cookies-refresh-batches" element={<CookiesRefreshBatches />} />
            <Route path="admin/cookies-refresh-batches/:batchId" element={<CookiesRefreshBatchDetailPage />} />
            <Route path="admin/api-cookie-renew-batches" element={<ApiCookieRenewBatches />} />
            <Route path="admin/api-cookie-renew-batches/:batchId" element={<ApiCookieRenewBatchDetailPage />} />
            <Route path="admin/close-notice-batches" element={<CloseNoticeBatches />} />
            <Route path="admin/close-notice-batches/:batchId" element={<CloseNoticeBatchDetailPage />} />
            <Route path="admin/red-flower-batches" element={<RedFlowerBatches />} />
            <Route path="admin/red-flower-batches/:batchId" element={<RedFlowerBatchDetailPage />} />
            <Route path="admin/scheduled-tasks" element={<ScheduledTasks />} />
          </Route>

          {/* Catch all */}
          <Route path="*" element={<Navigate to="/dashboard" replace />} />
        </Routes>
      </Suspense>
    </BrowserRouter>
  )
}

export default App
