import { createRoot } from 'react-dom/client'
import { createQueryClient } from '../../src/lib/query'
import { CameraHarness } from './camera-harness'

// Playwright serves this entry only from a disposable private bundle.
export function mountCameraHarness() {
  const client = createQueryClient()
  const container = document.createElement('div')
  document.body.append(container)
  const root = createRoot(container)
  root.render(<CameraHarness client={client} />)
  return () => { root.unmount(); container.remove(); client.clear() }
}
