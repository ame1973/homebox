export const messages = {
  app: {
    home: 'HomeBox 首頁', skip: '跳至主要內容', loading: '正在載入…',
    development: '開發版本：目前提供連線檢查，物品管理功能尚未開放。',
    notFound: '找不到此頁面', returnHome: '回到連線檢查',
  },
  theme: { label: '外觀', system: '跟隨系統', light: '淺色', dark: '深色' },
  connection: {
    title: '連上家中的 HomeBox',
    introduction: '連上家中的網路或 VPN，確認這部裝置能否連上家庭伺服器。',
    privacy: '連線檢查不會傳送物品或照片，也不會修改任何資料。',
    heading: '伺服器連線', pending: '正在檢查連線…',
    ok: '已連上 HomeBox', degraded: '伺服器回報異常',
    error: '暫時無法確認連線',
    okHelp: '伺服器已回應。這項檢查只確認連線，不代表所有功能均已就緒。',
    degradedHelp: '伺服器可以連線，但目前狀態異常。請檢查伺服器後再試一次。',
    errorHelp: '請確認家庭伺服器已啟動，且這部裝置已連上家中網路或 VPN，再重新檢查。',
    retry: '重新檢查', checking: '檢查中…', checked: '上次有效回應（裝置時間）',
    noResult: '尚未取得有效回應',
  },
} as const
