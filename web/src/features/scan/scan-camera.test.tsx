import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { messages } from '@/lib/messages'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import * as native from '@/lib/scan-native'
import { quoteExactText } from '@/lib/text-literal'
import { ScanCamera } from './scan-camera'

vi.mock('@/lib/scan-native', async (original) => ({ ...await original<typeof import('@/lib/scan-native')>(), createNativeBarcodeDecoder: vi.fn() }))
type Decoder = NonNullable<Awaited<ReturnType<typeof native.createNativeBarcodeDecoder>>>
const detect = vi.fn<Decoder['detect']>()
const decoder: Decoder = { formats: ['QR_CODE', 'EAN_13', 'UPC_A'], detect }
function Camera({ sessionActive }: { sessionActive: boolean }) { return <ScanCamera sessionActive={sessionActive} onUseCode={() => {}} /> }

const text = messages.scan
class FakeTrack extends EventTarget {
  readonly kind = 'video'
  readyState: 'live' | 'ended' = 'live'
  readonly stop = vi.fn(() => { this.readyState = 'ended' })
  readonly applyConstraints = vi.fn(async (_constraints?: MediaTrackConstraints) => {})
  readonly torch: boolean
  constructor(torch: boolean) { super(); this.torch = torch }
  getCapabilities() { return { torch: this.torch } }
}
function fakeStream(track: FakeTrack): MediaStream {
  return { getTracks: () => [track], getVideoTracks: () => [track], getAudioTracks: () => [] } as unknown as MediaStream
}
const acquire = vi.fn<(constraints?: MediaStreamConstraints) => Promise<MediaStream>>()
const originalDevices = Object.getOwnPropertyDescriptor(navigator, 'mediaDevices')
const originalSource = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, 'srcObject')
let visibility: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  acquire.mockReset()
  detect.mockReset()
  vi.mocked(native.createNativeBarcodeDecoder).mockReset().mockResolvedValue(undefined)
  vi.stubGlobal('isSecureContext', true)
  Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: { getUserMedia: acquire } })
  Object.defineProperty(HTMLMediaElement.prototype, 'srcObject', { configurable: true, writable: true, value: null })
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue()
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {})
  visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
})
afterEach(() => {
  if (originalDevices) Object.defineProperty(navigator, 'mediaDevices', originalDevices)
  else Reflect.deleteProperty(navigator, 'mediaDevices')
  if (originalSource) Object.defineProperty(HTMLMediaElement.prototype, 'srcObject', originalSource)
  else Reflect.deleteProperty(HTMLMediaElement.prototype, 'srcObject')
  vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals()
})

function readyVideo() {
  vi.spyOn(HTMLMediaElement.prototype, 'readyState', 'get').mockReturnValue(2)
  vi.spyOn(HTMLVideoElement.prototype, 'videoWidth', 'get').mockReturnValue(640)
  vi.spyOn(HTMLVideoElement.prototype, 'videoHeight', 'get').mockReturnValue(480)
}

it('reports the actual supported and missing native formats and retries only on request', async () => {
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValueOnce(undefined).mockResolvedValueOnce(decoder)
  render(<Camera sessionActive />)
  expect(await screen.findByText(text.nativeUnavailable)).toBeVisible()
  expect(native.createNativeBarcodeDecoder).toHaveBeenCalledOnce()
  expect(acquire).not.toHaveBeenCalled()
  await userEvent.setup().click(screen.getByRole('button', { name: text.nativeRetry }))
  expect(await screen.findByText(text.nativeFormats)).toBeVisible()
  expect(screen.getByText(/EAN-13／JAN/)).toHaveTextContent('UPC-A')
  expect(screen.getByText(/EAN-8/)).toHaveTextContent('Code 128')
  expect(native.createNativeBarcodeDecoder).toHaveBeenCalledTimes(2)
  expect(acquire).not.toHaveBeenCalled()
})

it('does not report missing formats when all required native formats are supported', async () => {
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue({ formats: ['QR_CODE', 'EAN_13', 'EAN_8', 'UPC_A', 'UPC_E', 'CODE_128'], detect })
  render(<Camera sessionActive />)
  expect(await screen.findByText(text.nativeFormats)).toBeVisible()
  expect(screen.queryByText(text.nativeMissing)).not.toBeInTheDocument()
})

it('shows independent exact results and offers only validated internal QR links', async () => {
  readyVideo()
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue(decoder)
  const raw = '\0é\r\n001', external = 'https://outside.example/u/secret', long = 'x'.repeat(513)
  const home = `${window.location.origin}/l/room`
  detect.mockResolvedValueOnce([{ format: 'UPC_A', rawValue: raw }, { format: 'QR_CODE', rawValue: home },
    { format: 'QR_CODE', rawValue: `${window.location.origin}/u/device%2B1` }, { format: 'QR_CODE', rawValue: external }, { format: 'QR_CODE', rawValue: long }])
  const track = new FakeTrack(false), choose = vi.fn()
  acquire.mockResolvedValueOnce(fakeStream(track))
  render(<MemoryRouter><ScanCamera sessionActive onUseCode={choose} /></MemoryRouter>)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  const results = await screen.findByRole('list', { name: text.nativeResults })
  expect(track.stop).toHaveBeenCalledOnce()
  expect(within(results).getAllByRole('listitem')).toHaveLength(5)
  expect(results).toHaveTextContent(quoteExactText(raw))
  expect(within(results).getByRole('link', { name: text.nativeOpenLocation })).toHaveAttribute('href', '/locations/room')
  expect(within(results).getByRole('link', { name: text.nativeOpenUnit })).toHaveAttribute('href', '/units/device%2B1')
  expect(within(results).getAllByText(text.nativeUntrustedQR)).toHaveLength(2)
  expect(within(results).getByText(text.nativeTooLong)).toBeVisible()
  expect(within(results).getAllByRole('link')).toHaveLength(2)
  expect(choose).not.toHaveBeenCalled()
  await userEvent.setup().click(within(within(results).getAllByRole('listitem')[0]!).getByRole('button', { name: text.nativeUse }))
  expect(choose).toHaveBeenCalledExactlyOnceWith({ format: 'UPC_A', rawValue: raw })
  expect(screen.queryByRole('list', { name: text.nativeResults })).not.toBeInTheDocument()
  expect(acquire).toHaveBeenCalledOnce()
})

it('reports truncated results and clears them without reopening the camera', async () => {
  readyVideo()
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue(decoder)
  detect.mockResolvedValueOnce(Array.from({ length: 21 }, (_, index) => ({ format: 'UPC_A' as const, rawValue: String(index) })))
  const track = new FakeTrack(false)
  acquire.mockResolvedValueOnce(fakeStream(track))
  render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  expect(await screen.findByText(text.nativeTruncated)).toBeVisible()
  expect(within(screen.getByRole('list', { name: text.nativeResults })).getAllByRole('listitem')).toHaveLength(20)
  await userEvent.setup().click(screen.getByRole('button', { name: text.nativeClear }))
  expect(screen.queryByRole('list', { name: text.nativeResults })).not.toBeInTheDocument()
  expect(track.stop).toHaveBeenCalledOnce()
  expect(acquire).toHaveBeenCalledOnce()
})

it('moves focus from disappearing camera controls to results and back when cleared', async () => {
  readyVideo()
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue(decoder)
  let finish!: (codes: native.ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  const track = new FakeTrack(false)
  acquire.mockResolvedValueOnce(fakeStream(track))
  render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  screen.getByRole('button', { name: text.cameraSwitch }).focus()
  await act(async () => { finish([{ format: 'UPC_A', rawValue: '001' }]); await Promise.resolve() })
  expect(await screen.findByRole('heading', { name: text.nativeResults })).toHaveFocus()
  await userEvent.setup().click(screen.getByRole('button', { name: text.nativeClear }))
  expect(screen.getByRole('button', { name: text.cameraStart })).toHaveFocus()
})

it('does not steal focus from outside the camera controls when a result arrives', async () => {
  readyVideo()
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue(decoder)
  let finish!: (codes: native.ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  acquire.mockResolvedValueOnce(fakeStream(new FakeTrack(false)))
  render(<><Camera sessionActive /><button type="button">Outside camera</button></>)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  screen.getByRole('button', { name: 'Outside camera' }).focus()
  await act(async () => { finish([{ format: 'UPC_A', rawValue: '001' }]); await Promise.resolve() })
  expect(await screen.findByRole('heading', { name: text.nativeResults })).toBeVisible()
  expect(screen.getByRole('button', { name: 'Outside camera' })).toHaveFocus()
})

it('pauses and resumes recognition without restarting the live camera', async () => {
  readyVideo()
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue(decoder)
  let finish!: (codes: native.ScannedCode[]) => void
  detect.mockReturnValueOnce(new Promise((resolve) => { finish = resolve })).mockResolvedValueOnce([{ format: 'UPC_A', rawValue: '001' }])
  const track = new FakeTrack(false)
  acquire.mockResolvedValueOnce(fakeStream(track))
  render(<MemoryRouter><Camera sessionActive /></MemoryRouter>)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  await userEvent.setup().click(screen.getByRole('button', { name: text.nativePause }))
  expect(screen.getByText(text.nativePaused)).toBeVisible()
  expect(track.readyState).toBe('live')
  await act(async () => { finish([{ format: 'UPC_A', rawValue: 'stale' }]); await Promise.resolve() })
  expect(screen.queryByRole('list', { name: text.nativeResults })).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: text.nativeResume }))
  expect(await screen.findByRole('list', { name: text.nativeResults })).toHaveTextContent('"001"')
  expect(track.stop).toHaveBeenCalledOnce()
  expect(acquire).toHaveBeenCalledOnce()
})

it('stops the camera on decoder failure without exposing native diagnostics', async () => {
  readyVideo()
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue(decoder)
  detect.mockRejectedValueOnce(new Error('private decoder diagnostics'))
  const track = new FakeTrack(false)
  acquire.mockResolvedValueOnce(fakeStream(track))
  render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  expect(await screen.findByText(text.nativeError)).toBeVisible()
  expect(track.stop).toHaveBeenCalledOnce()
  expect(document.body).not.toHaveTextContent('private decoder diagnostics')
  expect(screen.getByRole('button', { name: text.cameraStart })).toBeEnabled()
})

it('returns focus to camera start when an active focused control disappears on decoder failure', async () => {
  readyVideo()
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue(decoder)
  let fail!: (reason: Error) => void
  detect.mockReturnValueOnce(new Promise((_resolve, reject) => { fail = reject }))
  acquire.mockResolvedValueOnce(fakeStream(new FakeTrack(false)))
  render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await waitFor(() => expect(detect).toHaveBeenCalledOnce())
  screen.getByRole('button', { name: text.cameraSwitch }).focus()
  await act(async () => { fail(new Error('private diagnostics')); await Promise.resolve() })
  expect(await screen.findByText(text.nativeError)).toBeVisible()
  expect(screen.getByRole('button', { name: text.cameraStart })).toHaveFocus()
})

it('opens a real stream only after a gesture and stops the old track before switching', async () => {
  const rear = new FakeTrack(false), front = new FakeTrack(false), rearStream = fakeStream(rear), frontStream = fakeStream(front)
  acquire.mockResolvedValueOnce(rearStream).mockImplementationOnce(async () => { expect(rear.readyState).toBe('ended'); return frontStream })
  const view = render(<Camera sessionActive />)
  expect(acquire).not.toHaveBeenCalled()
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  expect(acquire).toHaveBeenCalledExactlyOnceWith({ audio: false, video: { facingMode: { ideal: 'environment' } } })
  const video = await screen.findByLabelText(text.cameraVideo) as HTMLVideoElement
  await waitFor(() => expect(video.srcObject).toBe(rearStream))
  expect(video.autoplay).toBe(true)
  expect(video.muted).toBe(true)
  expect(video.playsInline).toBe(true)
  expect(screen.queryByRole('button', { name: text.cameraTorchOn })).not.toBeInTheDocument()
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraSwitch }))
  expect(acquire).toHaveBeenLastCalledWith({ audio: false, video: { facingMode: { ideal: 'user' } } })
  await waitFor(() => expect((screen.getByLabelText(text.cameraVideo) as HTMLVideoElement).srcObject).toBe(frontStream))
  expect(rear.stop).toHaveBeenCalledOnce()
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStop }))
  expect(front.stop).toHaveBeenCalledOnce()
  expect(screen.queryByLabelText(text.cameraVideo)).not.toBeInTheDocument()
  view.unmount()
})

it('does not open a camera before a completed session and maps permission errors without private diagnostics', async () => {
  acquire.mockRejectedValueOnce(new DOMException('/private device details', 'NotAllowedError'))
  const view = render(<Camera sessionActive={false} />)
  expect(screen.getByRole('button', { name: text.cameraStart })).toBeDisabled()
  expect(screen.getByText(text.cameraSession)).toBeVisible()
  view.rerender(<Camera sessionActive />)
  expect(acquire).not.toHaveBeenCalled()
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  expect(await screen.findByText(text.cameraErrors['permission-denied'])).toBeVisible()
  expect(document.body).not.toHaveTextContent('/private device details')
  expect(acquire).toHaveBeenCalledOnce()
  view.unmount()
})

it.each(['hidden', 'pagehide', 'session', 'stop', 'unmount'] as const)('stops a stream returned late after %s', async (reason) => {
  let finish!: (stream: MediaStream) => void
  acquire.mockReturnValueOnce(new Promise((resolve) => { finish = resolve }))
  const view = render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  expect(screen.getByText(text.cameraWaiting)).toBeVisible()
  act(() => {
    if (reason === 'hidden') { visibility.mockReturnValue('hidden'); document.dispatchEvent(new Event('visibilitychange')) }
    if (reason === 'pagehide') window.dispatchEvent(new Event('pagehide'))
    if (reason === 'session') notifyPageSuspended()
    if (reason === 'stop') fireEvent.click(screen.getByRole('button', { name: text.cameraStop }))
    if (reason === 'unmount') view.unmount()
  })
  const track = new FakeTrack(false)
  await act(async () => { finish(fakeStream(track)); await Promise.resolve() })
  expect(track.stop).toHaveBeenCalledOnce()
  expect(screen.queryByLabelText(text.cameraVideo)).not.toBeInTheDocument()
  expect(acquire).toHaveBeenCalledOnce()
  if (reason !== 'unmount') view.unmount()
})

it('offers torch only when advertised and keeps failures safe and explicitly retryable', async () => {
  const track = new FakeTrack(true), stream = fakeStream(track)
  acquire.mockResolvedValueOnce(stream)
  const view = render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await screen.findByLabelText(text.cameraVideo)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraTorchOn }))
  expect(track.applyConstraints).toHaveBeenCalledWith({ advanced: [{ torch: true }] })
  expect(await screen.findByRole('button', { name: text.cameraTorchOff })).toBeEnabled()
  track.applyConstraints.mockRejectedValueOnce(new Error('/private torch diagnostic'))
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraTorchOff }))
  expect(await screen.findByText(text.cameraTorchError)).toBeVisible()
  expect(document.body).not.toHaveTextContent('/private torch diagnostic')
  expect(screen.getByRole('button', { name: text.cameraTorchOff })).toBeEnabled()
  expect(track.applyConstraints).toHaveBeenCalledTimes(2)
  view.unmount()
  expect(track.stop).toHaveBeenCalledOnce()
})

it('stops an active camera when the session becomes inactive and never reopens it automatically', async () => {
  const track = new FakeTrack(false)
  acquire.mockResolvedValueOnce(fakeStream(track))
  const view = render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await screen.findByLabelText(text.cameraVideo)
  view.rerender(<Camera sessionActive={false} />)
  expect(track.stop).toHaveBeenCalledOnce()
  expect(screen.queryByLabelText(text.cameraVideo)).not.toBeInTheDocument()
  view.rerender(<Camera sessionActive />)
  expect(acquire).toHaveBeenCalledOnce()
  expect(screen.getByRole('button', { name: text.cameraStart })).toBeEnabled()
  view.unmount()
})

it('ignores a late torch response after switching to a different stream', async () => {
  const rear = new FakeTrack(true), front = new FakeTrack(false)
  let finish!: () => void
  rear.applyConstraints.mockReturnValueOnce(new Promise<void>((resolve) => { finish = resolve }))
  acquire.mockResolvedValueOnce(fakeStream(rear)).mockResolvedValueOnce(fakeStream(front))
  const view = render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await userEvent.setup().click(await screen.findByRole('button', { name: text.cameraTorchOn }))
  expect(screen.getByRole('button', { name: text.cameraTorchOn })).toBeDisabled()
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraSwitch }))
  await waitFor(() => expect(rear.stop).toHaveBeenCalledOnce())
  await act(async () => { finish(); await Promise.resolve() })
  expect(screen.queryByRole('button', { name: text.cameraTorchOn })).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: text.cameraTorchOff })).not.toBeInTheDocument()
  expect(screen.queryByText(text.cameraTorchError)).not.toBeInTheDocument()
  view.unmount()
})

it('ignores an old playback failure after switching to a new stream', async () => {
  const rear = new FakeTrack(false), front = new FakeTrack(false)
  let fail!: (cause: Error) => void
  vi.mocked(HTMLMediaElement.prototype.play).mockReturnValueOnce(new Promise((_resolve, reject) => { fail = reject })).mockResolvedValueOnce()
  acquire.mockResolvedValueOnce(fakeStream(rear)).mockResolvedValueOnce(fakeStream(front))
  const view = render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await waitFor(() => expect(HTMLMediaElement.prototype.play).toHaveBeenCalledOnce())
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraSwitch }))
  await waitFor(() => expect(HTMLMediaElement.prototype.play).toHaveBeenCalledTimes(2))
  await act(async () => { fail(new Error('late private playback error')); await Promise.resolve() })
  expect(screen.queryByText(text.cameraPlaybackError)).not.toBeInTheDocument()
  expect(document.body).not.toHaveTextContent('late private playback error')
  view.unmount()
})

it('offers a safe explicit playback retry without reopening the camera', async () => {
  const track = new FakeTrack(false)
  acquire.mockResolvedValueOnce(fakeStream(track))
  vi.mocked(HTMLMediaElement.prototype.play).mockRejectedValueOnce(new DOMException('private playback diagnostic', 'NotAllowedError')).mockResolvedValueOnce()
  const view = render(<Camera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  expect(await screen.findByText(text.cameraPlaybackError)).toBeVisible()
  expect(document.body).not.toHaveTextContent('private playback diagnostic')
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraPlaybackRetry }))
  await waitFor(() => expect(screen.queryByText(text.cameraPlaybackError)).not.toBeInTheDocument())
  expect(acquire).toHaveBeenCalledOnce()
  view.unmount()
})

// The timeout gate intentionally lasts until module reload; run this last.
it('keeps a hung detection latched after stopping its camera until page reload', async () => {
  vi.useFakeTimers()
  readyVideo()
  vi.mocked(native.createNativeBarcodeDecoder).mockResolvedValue(decoder)
  detect.mockReturnValueOnce(new Promise(() => {}))
  const first = new FakeTrack(false), second = new FakeTrack(false)
  acquire.mockResolvedValueOnce(fakeStream(first)).mockResolvedValueOnce(fakeStream(second))
  const view = render(<Camera sessionActive />)
  await act(async () => { await Promise.resolve() })
  fireEvent.click(screen.getByRole('button', { name: text.cameraStart }))
  await act(async () => { await Promise.resolve() })
  await act(async () => { await vi.advanceTimersByTimeAsync(0) })
  expect(detect).toHaveBeenCalledOnce()
  await act(async () => { await vi.advanceTimersByTimeAsync(8_000) })
  expect(screen.getByText(text.nativeTimeout)).toBeVisible()
  expect(first.stop).toHaveBeenCalledOnce()
  fireEvent.click(screen.getByRole('button', { name: text.cameraStart }))
  await act(async () => { await Promise.resolve() })
  await act(async () => { await vi.advanceTimersByTimeAsync(1_000) })
  expect(screen.getByText(text.nativeTimeout)).toBeVisible()
  expect(detect).toHaveBeenCalledOnce()
  expect(second.readyState).toBe('live')
  view.unmount()
  expect(second.stop).toHaveBeenCalledOnce()
})
