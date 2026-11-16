import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { messages } from '@/lib/messages'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import { ScanCamera } from './scan-camera'

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
  vi.restoreAllMocks(); vi.unstubAllGlobals()
})

it('opens a real stream only after a gesture and stops the old track before switching', async () => {
  const rear = new FakeTrack(false), front = new FakeTrack(false), rearStream = fakeStream(rear), frontStream = fakeStream(front)
  acquire.mockResolvedValueOnce(rearStream).mockImplementationOnce(async () => { expect(rear.readyState).toBe('ended'); return frontStream })
  const view = render(<ScanCamera sessionActive />)
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
  const view = render(<ScanCamera sessionActive={false} />)
  expect(screen.getByRole('button', { name: text.cameraStart })).toBeDisabled()
  expect(screen.getByText(text.cameraSession)).toBeVisible()
  view.rerender(<ScanCamera sessionActive />)
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
  const view = render(<ScanCamera sessionActive />)
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
  const view = render(<ScanCamera sessionActive />)
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
  const view = render(<ScanCamera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  await screen.findByLabelText(text.cameraVideo)
  view.rerender(<ScanCamera sessionActive={false} />)
  expect(track.stop).toHaveBeenCalledOnce()
  expect(screen.queryByLabelText(text.cameraVideo)).not.toBeInTheDocument()
  view.rerender(<ScanCamera sessionActive />)
  expect(acquire).toHaveBeenCalledOnce()
  expect(screen.getByRole('button', { name: text.cameraStart })).toBeEnabled()
  view.unmount()
})

it('ignores a late torch response after switching to a different stream', async () => {
  const rear = new FakeTrack(true), front = new FakeTrack(false)
  let finish!: () => void
  rear.applyConstraints.mockReturnValueOnce(new Promise<void>((resolve) => { finish = resolve }))
  acquire.mockResolvedValueOnce(fakeStream(rear)).mockResolvedValueOnce(fakeStream(front))
  const view = render(<ScanCamera sessionActive />)
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
  const view = render(<ScanCamera sessionActive />)
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
  const view = render(<ScanCamera sessionActive />)
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraStart }))
  expect(await screen.findByText(text.cameraPlaybackError)).toBeVisible()
  expect(document.body).not.toHaveTextContent('private playback diagnostic')
  await userEvent.setup().click(screen.getByRole('button', { name: text.cameraPlaybackRetry }))
  await waitFor(() => expect(screen.queryByText(text.cameraPlaybackError)).not.toBeInTheDocument())
  expect(acquire).toHaveBeenCalledOnce()
  view.unmount()
})
