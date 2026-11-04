import { useLayoutEffect } from 'react'
import { Monitor, Moon, Sun } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuRadioGroup,
  DropdownMenuRadioItem, DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { isTheme, useTheme, type Theme } from '@/lib/theme'
import { messages } from '@/lib/messages'

export function ThemeMenu() {
  const { theme, setTheme } = useTheme()
  useLayoutEffect(() => {
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const apply = () => {
      const dark = theme === 'dark' || (theme === 'system' && media.matches)
      document.documentElement.classList.toggle('dark', dark)
      document.documentElement.dataset.theme = dark ? 'dark' : 'light'
      document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
    }
    apply()
    media.addEventListener('change', apply)
    return () => media.removeEventListener('change', apply)
  }, [theme])

  const options = [
    { value: 'system', Icon: Monitor }, { value: 'light', Icon: Sun }, { value: 'dark', Icon: Moon },
  ] satisfies { value: Theme; Icon: typeof Monitor }[]
  const Icon = theme === 'system' ? Monitor : theme === 'dark' ? Moon : Sun
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" className="min-h-11 gap-2 px-3 text-sm focus-visible:ring-ring">
          <Icon aria-hidden="true" />{messages.theme.label}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-44">
        <DropdownMenuRadioGroup value={theme} onValueChange={(value) => { if (isTheme(value)) setTheme(value) }} aria-label={messages.theme.label}>
          {options.map(({ value, Icon: OptionIcon }) => (
            <DropdownMenuRadioItem key={value} value={value} className="min-h-12 gap-3 px-3 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
              <OptionIcon aria-hidden="true" />{messages.theme[value]}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
