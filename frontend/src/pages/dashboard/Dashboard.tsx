import { useEffect, useState } from 'react'
import { motion } from 'framer-motion'
import { Activity, Calendar, Clock, MessageSquare, Package, RefreshCw, ShoppingCart, Users } from 'lucide-react'
import { getAccountStats } from '@/api/accounts'
import { OrderAmountChart } from '@/components/common/OrderAmountChart'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'

interface DashboardStats {
  totalAccounts: number
  totalKeywords: number
  activeAccounts: number
  totalOrders: number
  todayReplyCount: number
  yesterdayReplyCount: number
  usedAccountCount: number
}

const emptyStats: DashboardStats = {
  totalAccounts: 0,
  totalKeywords: 0,
  activeAccounts: 0,
  totalOrders: 0,
  todayReplyCount: 0,
  yesterdayReplyCount: 0,
  usedAccountCount: 0,
}

export function Dashboard() {
  const { addToast } = useUIStore()
  const { isAuthenticated, token, _hasHydrated } = useAuthStore()
  const [loading, setLoading] = useState(true)
  const [stats, setStats] = useState<DashboardStats>(emptyStats)

  const loadDashboard = async () => {
    if (!_hasHydrated || !isAuthenticated || !token) return
    setLoading(true)
    try {
      const data = await getAccountStats()
      setStats({
        totalAccounts: data.total_accounts,
        totalKeywords: data.total_keywords,
        activeAccounts: data.active_accounts,
        totalOrders: data.total_orders,
        todayReplyCount: data.today_reply_count,
        yesterdayReplyCount: data.yesterday_reply_count,
        usedAccountCount: data.used_account_count,
      })
    } catch {
      addToast({ type: 'error', message: '加载统计数据失败' })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { void loadDashboard() }, [_hasHydrated, isAuthenticated, token])

  const cards = [
    { icon: Users, label: '闲鱼账号', value: stats.totalAccounts, color: 'stat-icon-primary' },
    { icon: Activity, label: '启用账号', value: stats.activeAccounts, color: 'stat-icon-success' },
    { icon: MessageSquare, label: '回复关键词', value: stats.totalKeywords, color: 'stat-icon-warning' },
    { icon: ShoppingCart, label: '累计订单', value: stats.totalOrders, color: 'stat-icon-info' },
    { icon: Calendar, label: '今日回复', value: stats.todayReplyCount, color: 'stat-icon-success' },
    { icon: Clock, label: '昨日回复', value: stats.yesterdayReplyCount, color: 'stat-icon-info' },
    { icon: Package, label: '已配置账号', value: stats.usedAccountCount, color: 'stat-icon-primary' },
  ]

  return (
    <div className="space-y-4">
      <div className="page-header flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div><h1 className="page-title">仪表盘</h1><p className="page-description">个人账号运行概览</p></div>
        <button onClick={loadDashboard} className="btn-ios-secondary" disabled={loading}><RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />刷新数据</button>
      </div>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-7">
        {cards.map((card, index) => {
          const Icon = card.icon
          return (
            <motion.div key={card.label} initial={{ opacity: 0, y: 12 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: index * 0.04 }} className="stat-card min-w-0">
              <div className={card.color}><Icon className="h-6 w-6" /></div>
              <div className="min-w-0"><p className="stat-value truncate">{loading ? '-' : card.value}</p><p className="stat-label truncate">{card.label}</p></div>
            </motion.div>
          )
        })}
      </div>
      <motion.div initial={{ opacity: 0, y: 12 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 0.12 }}><OrderAmountChart /></motion.div>
    </div>
  )
}
