import { useState } from 'react'
import { Link, createMemoryRouter, RouterProvider } from 'react-router'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { messages } from '@/lib/messages'
import { notifyPageSuspended } from '@/lib/session-lifecycle'
import * as frames from '@/lib/capture-frame'
import { CaptureWorkspace } from './capture-workspace'

const text = messages.capture, camera = messages.scan
class Track extends EventTarget {
  readonly kind = 'video'
  readyState: 'live' | 'ended' = 'live'
  readonly stop = vi.fn(() => { this.readyState = 'ended' })
}
const tracks: Track[] = []
const acquire = vi.fn(async () => {
  const track = new Track()
  tracks.push(track)
  return { getTracks: () => [track], getVideoTracks: () => [track], getAudioTracks: () => [] } as unknown as MediaStream
})
class PhotoWorker {
  static instances: PhotoWorker[] = []
  static replies: unknown[] = []
  static hold = false
  onmessage: ((event: MessageEvent<unknown>) => void) | null = null
  onerror: ((event: ErrorEvent) => void) | null = null
  onmessageerror: (() => void) | null = null
  terminated = false
  readonly messages: unknown[] = []
  constructor(_url: URL, _options: WorkerOptions) { PhotoWorker.instances.push(this) }
  postMessage(message: unknown) {
    this.messages.push(message)
    const reply = PhotoWorker.replies.shift() ?? { type: 'result', kind: 'prepared', mime: 'image/png', width: 4, height: 3,
      orientation: 1, blob: new Blob(['minimized'], { type: 'image/png' }) }
    if (!PhotoWorker.hold) queueMicrotask(() => this.emit(reply))
  }
  emit(value: unknown) { this.onmessage?.({ data: value } as MessageEvent<unknown>) }
  terminate() { this.terminated = true }
}
const createURL = vi.fn(), revokeURL = vi.fn()
const disposers: (() => void)[] = []
const oldDevices = Object.getOwnPropertyDescriptor(navigator, 'mediaDevices')
const oldSource = Object.getOwnPropertyDescriptor(HTMLMediaElement.prototype, 'srcObject')
const oldCreate = Object.getOwnPropertyDescriptor(URL, 'createObjectURL')
const oldRevoke = Object.getOwnPropertyDescriptor(URL, 'revokeObjectURL')
let visibility: ReturnType<typeof vi.spyOn>, encoding: ReturnType<typeof vi.spyOn>, created = 0

beforeEach(() => {
  tracks.length = 0; created = 0; acquire.mockClear(); createURL.mockReset(); revokeURL.mockReset()
  PhotoWorker.instances = []; PhotoWorker.replies = []; PhotoWorker.hold = false
  vi.stubGlobal('Worker', PhotoWorker)
  vi.stubGlobal('createImageBitmap', vi.fn(async () => ({ width: 4, height: 3, close: vi.fn() })))
  createURL.mockImplementation(() => `blob:homebox-test/${++created}`)
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, writable: true, value: createURL })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, writable: true, value: revokeURL })
  Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: { getUserMedia: acquire } })
  Object.defineProperty(HTMLMediaElement.prototype, 'srcObject', { configurable: true, writable: true, value: null })
  vi.stubGlobal('isSecureContext', true)
  visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.spyOn(HTMLMediaElement.prototype, 'readyState', 'get').mockReturnValue(2)
  vi.spyOn(HTMLVideoElement.prototype, 'videoWidth', 'get').mockReturnValue(640)
  vi.spyOn(HTMLVideoElement.prototype, 'videoHeight', 'get').mockReturnValue(480)
  vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue()
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {})
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation((() => ({ drawImage: vi.fn(), setTransform: vi.fn(), imageSmoothingEnabled: false } as unknown as CanvasRenderingContext2D)) as unknown as typeof HTMLCanvasElement.prototype.getContext)
  encoding = vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation((callback, type) => callback(new Blob(['image'], { type: type ?? 'image/png' })))
})
afterEach(() => {
  for (const dispose of disposers.splice(0)) dispose()
  if (oldDevices) Object.defineProperty(navigator, 'mediaDevices', oldDevices); else Reflect.deleteProperty(navigator, 'mediaDevices')
  if (oldSource) Object.defineProperty(HTMLMediaElement.prototype, 'srcObject', oldSource); else Reflect.deleteProperty(HTMLMediaElement.prototype, 'srcObject')
  if (oldCreate) Object.defineProperty(URL, 'createObjectURL', oldCreate); else Reflect.deleteProperty(URL, 'createObjectURL')
  if (oldRevoke) Object.defineProperty(URL, 'revokeObjectURL', oldRevoke); else Reflect.deleteProperty(URL, 'revokeObjectURL')
  vi.restoreAllMocks(); vi.unstubAllGlobals()
})

function mount() {
  function Harness() {
    const [active, setActive] = useState(true)
    return <><Link to="/next">Next page</Link><button type="button" onClick={() => setActive((value) => !value)}>Toggle session check</button><button type="button">Outside focus</button><CaptureWorkspace active={active} /></>
  }
  const router = createMemoryRouter([{ path: '/', element: <Harness /> }, { path: '/next', element: <h1>Next page reached</h1> }])
  const view = render(<RouterProvider router={router} />)
  disposers.push(() => { view.unmount(); router.dispose() })
  return { router }
}
async function openAndCapture() {
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  await user.click(await screen.findByRole('button', { name: text.take }))
  await screen.findByRole('img', { name: text.preview })
}
function previewImage() { return screen.getByRole('img', { name: text.preview }).querySelector('image')! }

const selectedFile = (name = 'PRIVATE-NAME.png') => new File(['selected image bytes'], name, { type: 'image/png', lastModified: 123 })

it('offers explicit local file selection as a secondary camera fallback and imports multiple photos', async () => {
  mount()
  const user = userEvent.setup(), input = screen.getByLabelText(text.fileInput)
  expect(input).toHaveAttribute('type', 'file'); expect(input).toHaveAttribute('multiple')
  expect(input).toHaveAttribute('accept', 'image/jpeg,image/png,image/webp')
  expect(screen.getByText(text.fileQuality)).toBeVisible()
  expect(acquire).not.toHaveBeenCalled(); expect(PhotoWorker.instances).toHaveLength(0)
  await user.upload(input, [selectedFile('private-one.png'), selectedFile('private-two.png')])
  await waitFor(() => expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button')).toHaveLength(2))
  expect(input).toHaveValue(''); expect(PhotoWorker.instances).toHaveLength(2)
  expect(PhotoWorker.instances.every((worker) => worker.terminated)).toBe(true)
  expect(acquire).not.toHaveBeenCalled(); expect(createURL).toHaveBeenCalledTimes(2)
  expect(screen.getByRole('img', { name: text.preview })).toHaveAttribute('viewBox', '0 0 4 3')
  expect(document.body).not.toHaveTextContent('private-one.png'); expect(document.body).not.toHaveTextContent('private-two.png')
  expect(screen.getByText(`${text.filesAdded}2`)).toBeVisible()
})

it('stops a partially successful batch on an invalid file without automatically processing the rest', async () => {
  const good = { type: 'result', kind: 'prepared', mime: 'image/png', width: 4, height: 3, orientation: 1, blob: new Blob(['minimized'], { type: 'image/png' }) }
  PhotoWorker.replies = [good, { type: 'result', kind: 'unsupported' }, good]
  mount()
  const user = userEvent.setup(), input = screen.getByLabelText(text.fileInput)
  await user.upload(input, [selectedFile('one.png'), selectedFile('two.png'), selectedFile('three.png')])
  expect(await screen.findByText(text.fileErrors.unsupported)).toBeVisible()
  expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button')).toHaveLength(1)
  expect(screen.getByText(text.fileRemaining)).toBeVisible()
  expect(PhotoWorker.instances).toHaveLength(2); expect(createURL).toHaveBeenCalledTimes(1)
  expect(document.body).not.toHaveTextContent('two.png')
  await user.upload(input, selectedFile('three.png'))
  await waitFor(() => expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button')).toHaveLength(2))
  expect(PhotoWorker.instances).toHaveLength(3)
})

it('rejects an oversized selection before starting any worker and retains camera access', async () => {
  mount()
  const input = screen.getByLabelText(text.fileInput), files = Array.from({ length: 11 }, (_, index) => selectedFile(`${index}.png`))
  await userEvent.setup().upload(input, files)
  expect(await screen.findByText(text.fileErrors['count-limit'])).toBeVisible()
  expect(PhotoWorker.instances).toHaveLength(0); expect(acquire).not.toHaveBeenCalled()
  expect(screen.getByRole('button', { name: camera.cameraStart })).toBeEnabled()
})

it('bounds FileList references before copying an oversized selection', () => {
  mount()
  const input = screen.getByLabelText(text.fileInput)
  Object.defineProperty(input, 'files', { configurable: true, value: { length: 11, [Symbol.iterator]() { throw new Error('Selection must not be copied') } } })
  fireEvent.change(input)
  expect(screen.getByText(text.fileErrors['count-limit'])).toBeVisible()
  expect(PhotoWorker.instances).toHaveLength(0)
})

it('rejects an empty file before reading it', async () => {
  mount()
  await userEvent.setup().upload(screen.getByLabelText(text.fileInput), new File([], 'empty.png', { type: 'image/png' }))
  expect(await screen.findByText(text.fileErrors.invalid)).toBeVisible()
  expect(PhotoWorker.instances).toHaveLength(0)
})

it('rejects an oversized individual file before reading it', async () => {
  mount()
  const file = selectedFile('private-too-large.png')
  Object.defineProperty(file, 'size', { value: frames.maxFrameBytes + 1 })
  await userEvent.setup().upload(screen.getByLabelText(text.fileInput), file)
  expect(await screen.findByText(text.fileErrors['too-large'])).toBeVisible()
  expect(PhotoWorker.instances).toHaveLength(0); expect(createURL).not.toHaveBeenCalled()
  expect(document.body).not.toHaveTextContent('private-too-large.png')
})

it('disables the file fallback while the live camera is running', async () => {
  mount()
  const user = userEvent.setup(), input = screen.getByLabelText(text.fileInput)
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  expect(await screen.findByRole('button', { name: camera.cameraStop })).toBeVisible()
  expect(input).toBeDisabled()
  await user.click(screen.getByRole('button', { name: camera.cameraStop }))
  expect(input).toBeEnabled(); expect(PhotoWorker.instances).toHaveLength(0)
})

it('can explicitly select the same file again after a successful import', async () => {
  mount()
  const user = userEvent.setup(), input = screen.getByLabelText(text.fileInput), file = selectedFile()
  await user.upload(input, file)
  await waitFor(() => expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button')).toHaveLength(1))
  await user.upload(input, file)
  await waitFor(() => expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button')).toHaveLength(2))
  expect(PhotoWorker.instances).toHaveLength(2)
})

it('does not steal focus moved elsewhere while a selected file is processing', async () => {
  PhotoWorker.hold = true; mount()
  const user = userEvent.setup()
  await user.upload(screen.getByLabelText(text.fileInput), selectedFile())
  const elsewhere = screen.getByRole('button', { name: 'Outside focus' }); elsewhere.focus()
  expect(elsewhere).toHaveFocus()
  act(() => { PhotoWorker.instances[0]!.emit({ type: 'result', kind: 'prepared', mime: 'image/png', width: 4, height: 3, orientation: 1, blob: new Blob(['minimized'], { type: 'image/png' }) }) })
  expect(await screen.findByRole('img', { name: text.preview })).toBeVisible()
  expect(elsewhere).toHaveFocus()
})

it('does not move focus back after a pointer interaction outside the processing controls', async () => {
  PhotoWorker.hold = true; mount()
  const input = screen.getByLabelText(text.fileInput)
  input.focus()
  fireEvent.change(input, { target: { files: [selectedFile()] } })
  const elsewhere = screen.getByRole('button', { name: 'Outside focus' })
  elsewhere.focus(); elsewhere.blur(); fireEvent.pointerDown(document.body)
  expect(document.body).toHaveFocus()
  act(() => { PhotoWorker.instances[0]!.emit({ type: 'result', kind: 'prepared', mime: 'image/png', width: 4, height: 3, orientation: 1, blob: new Blob(['minimized'], { type: 'image/png' }) }) })
  expect(await screen.findByRole('img', { name: text.preview })).toBeVisible()
  expect(document.body).toHaveFocus()
})

it('cancels selected-file processing on session revalidation without replaying it', async () => {
  PhotoWorker.hold = true; mount()
  const user = userEvent.setup()
  await user.upload(screen.getByLabelText(text.fileInput), selectedFile())
  const worker = PhotoWorker.instances[0]!
  await user.click(screen.getByRole('button', { name: 'Toggle session check' }))
  expect(worker.terminated).toBe(true); expect(screen.getByText(text.inactive)).toBeVisible()
  await user.click(screen.getByRole('button', { name: 'Toggle session check' }))
  expect(screen.getByText(text.fileCancelled)).toBeVisible()
  expect(PhotoWorker.instances).toHaveLength(1); expect(createURL).not.toHaveBeenCalled()
})

it('discards an uncancelable late decoded bitmap after file cancellation', async () => {
  let release!: (value: ImageBitmap) => void
  const closed = vi.fn(), pendingBitmap = new Promise<ImageBitmap>((resolve) => { release = resolve })
  vi.mocked(createImageBitmap).mockReturnValueOnce(pendingBitmap)
  mount()
  const user = userEvent.setup()
  await user.upload(screen.getByLabelText(text.fileInput), selectedFile())
  await waitFor(() => expect(createImageBitmap).toHaveBeenCalledOnce())
  await user.click(screen.getByRole('button', { name: text.fileCancel }))
  expect(screen.getByText(text.empty)).toBeVisible()
  await act(async () => { release({ width: 4, height: 3, close: closed } as unknown as ImageBitmap) })
  await waitFor(() => expect(closed).toHaveBeenCalledOnce())
  expect(createURL).not.toHaveBeenCalled()
})

it('cancels a pending selected file, ignores late worker replies and restores focus', async () => {
  PhotoWorker.hold = true; mount()
  const user = userEvent.setup(), input = screen.getByLabelText(text.fileInput)
  await user.upload(input, selectedFile())
  expect(await screen.findByText(/1\s*\/\s*1/)).toBeVisible()
  const worker = PhotoWorker.instances[0]!, cancel = screen.getByRole('button', { name: text.fileCancel })
  await user.click(cancel)
  expect(worker.terminated).toBe(true); expect(input).toHaveFocus()
  act(() => { worker.emit({ type: 'result', kind: 'prepared', mime: 'image/png', width: 4, height: 3, orientation: 1, blob: new Blob(['late'], { type: 'image/png' }) }) })
  expect(createURL).not.toHaveBeenCalled(); expect(screen.getByText(text.empty)).toBeVisible()
})

it('clears prior photos and pending selected files on a delivered session-clear signal', async () => {
  mount(); await openAndCapture()
  PhotoWorker.hold = true
  await userEvent.setup().upload(screen.getByLabelText(text.fileInput), selectedFile())
  const worker = PhotoWorker.instances[0]!
  act(() => { notifyPageSuspended() })
  expect(worker.terminated).toBe(true); expect(revokeURL).toHaveBeenCalledWith('blob:homebox-test/1')
  expect(screen.queryByRole('img', { name: text.preview })).not.toBeInTheDocument()
  act(() => { worker.emit({ type: 'result', kind: 'prepared', mime: 'image/png', width: 4, height: 3, orientation: 1, blob: new Blob(['late'], { type: 'image/png' }) }) })
  expect(createURL).toHaveBeenCalledOnce()
})

it('requires a gesture and edits exact original-pixel crop and rotation without another encode', async () => {
  mount()
  expect(acquire).not.toHaveBeenCalled()
  expect(screen.getByText(text.empty)).toBeVisible()
  await openAndCapture()
  expect(acquire).toHaveBeenCalledExactlyOnceWith({ audio: false, video: { facingMode: { ideal: 'environment' } } })
  expect(tracks[0]?.stop).toHaveBeenCalledOnce()
  expect(screen.getByRole('img', { name: text.preview })).toHaveAttribute('viewBox', '0 0 640 480')
  expect(previewImage()).toHaveAttribute('transform', 'matrix(1 0 0 1 0 0)')
  expect(previewImage()).toHaveAttribute('href', 'blob:homebox-test/1')
  expect(screen.getByRole('heading', { name: `${text.selected}：${text.photo} 1` })).toHaveFocus()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.rotateRight }))
  expect(screen.getByRole('img', { name: text.preview })).toHaveAttribute('viewBox', '0 0 480 640')
  expect(previewImage()).toHaveAttribute('transform', 'matrix(0 1 -1 0 480 0)')
  for (const [label, value] of [[text.cropX, '10'], [text.cropY, '20'], [text.cropWidth, '100'], [text.cropHeight, '200']] as const) {
    await user.type(screen.getByRole('textbox', { name: label }), value)
  }
  await user.click(screen.getByRole('button', { name: text.crop }))
  expect(screen.getByRole('img', { name: text.preview })).toHaveAttribute('viewBox', '0 0 100 200')
  expect(previewImage()).toHaveAttribute('transform', 'matrix(0 1 -1 0 470 -20)')
  expect(encoding).toHaveBeenCalledOnce()
  await user.click(screen.getByRole('button', { name: text.crop }))
  expect(screen.getByRole('alert')).toHaveTextContent(text.cropError)
  expect(screen.getByRole('textbox', { name: text.cropWidth })).toHaveValue('100')
  await user.click(screen.getByRole('button', { name: text.reset }))
  expect(screen.getByRole('img', { name: text.preview })).toHaveAttribute('viewBox', '0 0 640 480')
  expect(previewImage()).toHaveAttribute('href', 'blob:homebox-test/1')
  await user.click(screen.getByRole('button', { name: text.rotateRight }))
  await user.click(screen.getByRole('button', { name: text.rotateRight }))
  expect(previewImage()).toHaveAttribute('transform', 'matrix(-1 0 0 -1 640 480)')
  await user.click(screen.getByRole('button', { name: text.rotateRight }))
  expect(previewImage()).toHaveAttribute('transform', 'matrix(0 -1 1 0 0 640)')
  await user.click(screen.getByRole('button', { name: text.rotateLeft }))
  expect(previewImage()).toHaveAttribute('transform', 'matrix(-1 0 0 -1 640 480)')
  expect(encoding).toHaveBeenCalledOnce()
})

it('stops before switching cameras and closes the replacement on request', async () => {
  mount()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  await user.click(await screen.findByRole('button', { name: camera.cameraSwitch }))
  expect(tracks[0]?.stop).toHaveBeenCalledOnce()
  expect(acquire).toHaveBeenLastCalledWith({ audio: false, video: { facingMode: { ideal: 'user' } } })
  await waitFor(() => expect(tracks).toHaveLength(2))
  await user.click(screen.getByRole('button', { name: camera.cameraStop }))
  expect(tracks[1]?.stop).toHaveBeenCalledOnce()
  expect(screen.queryByLabelText(camera.cameraVideo)).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: camera.cameraStart })).toHaveFocus()
  expect(createURL).not.toHaveBeenCalled()
})

it('maps permission failures without exposing raw browser diagnostics', async () => {
  acquire.mockRejectedValueOnce(new DOMException('private camera identifier', 'NotAllowedError'))
  mount()
  await userEvent.setup().click(screen.getByRole('button', { name: camera.cameraStart }))
  expect(await screen.findByRole('alert')).toHaveTextContent(camera.cameraErrors['permission-denied'])
  expect(screen.queryByText('private camera identifier')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: camera.cameraStart })).toBeEnabled()
  expect(createURL).not.toHaveBeenCalled()
})

it('retries failed video playback only on explicit action', async () => {
  vi.mocked(HTMLMediaElement.prototype.play).mockRejectedValueOnce(new Error('private playback error')).mockResolvedValueOnce(undefined)
  mount()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  expect(await screen.findByText(camera.cameraPlaybackError)).toBeVisible()
  expect(screen.queryByText('private playback error')).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: camera.cameraPlaybackRetry }))
  await waitFor(() => expect(screen.queryByText(camera.cameraPlaybackError)).not.toBeInTheDocument())
  await user.click(screen.getByRole('button', { name: camera.cameraStop }))
  expect(tracks[0]?.stop).toHaveBeenCalledOnce()
})

it('does not steal focus moved elsewhere while an uncancelable frame is encoding', async () => {
  let complete!: BlobCallback
  encoding.mockImplementationOnce((callback: BlobCallback) => { complete = callback })
  mount()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  await user.click(await screen.findByRole('button', { name: text.take }))
  const elsewhere = screen.getByRole('button', { name: 'Outside focus' })
  elsewhere.focus()
  expect(elsewhere).toHaveFocus()
  await act(async () => { complete(new Blob(['photo'], { type: 'image/jpeg' })) })
  expect(await screen.findByRole('img', { name: text.preview })).toBeVisible()
  expect(elsewhere).toHaveFocus()
})

it('focuses the confirmation heading and handles the first Escape without retaking', async () => {
  mount(); await openAndCapture()
  const user = userEvent.setup(), button = screen.getByRole('button', { name: text.retake })
  await user.click(button)
  const dialog = screen.getByRole('alertdialog')
  expect(within(dialog).getByRole('heading', { name: text.retakeTitle })).toHaveFocus()
  fireEvent.keyDown(dialog, { key: 'Escape' })
  await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
  expect(button).toHaveFocus()
  expect(acquire).toHaveBeenCalledOnce()
  expect(screen.getByRole('img', { name: text.preview })).toBeVisible()
})

it('retains description, explicit cover and order on confirmed retake and clears a deleted cover', async () => {
  mount(); await openAndCapture()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: text.setCover }))
  await user.type(screen.getByRole('textbox', { name: text.description }), 'kept description')
  await openAndCapture()
  await user.click(screen.getByRole('button', { name: `${text.photo} 1（${text.cover}）` }))
  await user.click(screen.getByRole('button', { name: text.later }))
  expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button').map((button) => button.textContent)).toEqual([`${text.photo} 1`, `${text.photo} 2（${text.cover}）`])
  await user.click(screen.getByRole('button', { name: text.retake }))
  expect(acquire).toHaveBeenCalledTimes(2)
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmRetake }))
  await user.click(await screen.findByRole('button', { name: text.take }))
  await waitFor(() => expect(previewImage()).toHaveAttribute('href', 'blob:homebox-test/3'))
  expect(revokeURL).toHaveBeenCalledWith('blob:homebox-test/1')
  expect(screen.getByRole('textbox', { name: text.description })).toHaveValue('kept description')
  expect(screen.getByRole('button', { name: text.clearCover })).toHaveAttribute('aria-pressed', 'true')
  expect(screen.getByRole('heading', { name: `${text.selected}：${text.photo} 2` })).toBeVisible()
  await user.click(screen.getByRole('button', { name: text.remove }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmRemove }))
  expect(revokeURL).toHaveBeenCalledWith('blob:homebox-test/3')
  expect(screen.getByRole('button', { name: text.setCover })).toHaveAttribute('aria-pressed', 'false')
  expect(screen.getByRole('heading', { name: `${text.selected}：${text.photo} 1` })).toBeVisible()
})

it('retains temporarily invalid raw descriptions and reports the actual writing bound', async () => {
  mount(); await openAndCapture()
  const input = screen.getByRole('textbox', { name: text.description })
  fireEvent.change(input, { target: { value: '\ud800' } })
  expect(input).toHaveValue('\ud800')
  expect(input).toHaveAttribute('aria-invalid', 'true')
  expect(screen.getByRole('alert')).toHaveTextContent(text.descriptionError)
})

it('purges old and pending frames synchronously on backgrounding and rejects a late callback', async () => {
  mount(); await openAndCapture()
  let complete!: BlobCallback
  encoding.mockImplementationOnce((callback: BlobCallback) => { complete = callback })
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  await user.click(await screen.findByRole('button', { name: text.take }))
  expect(screen.getByText(text.taking)).toBeVisible()
  visibility.mockReturnValue('hidden')
  act(() => { document.dispatchEvent(new Event('visibilitychange')) })
  expect(tracks[1]?.stop).toHaveBeenCalledOnce()
  expect(revokeURL).toHaveBeenCalledWith('blob:homebox-test/1')
  expect(screen.queryByRole('img', { name: text.preview })).not.toBeInTheDocument()
  expect(screen.getByText(text.backgroundCleared)).toBeVisible()
  act(() => { complete(new Blob(['late'], { type: 'image/jpeg' })) })
  expect(createURL).toHaveBeenCalledOnce()
  expect(screen.getByText(text.empty)).toBeVisible()
})

it('clears the album and latches the camera on a delivered session-clear signal', async () => {
  mount(); await openAndCapture()
  act(() => { notifyPageSuspended() })
  expect(revokeURL).toHaveBeenCalledWith('blob:homebox-test/1')
  expect(screen.getByText(text.empty)).toBeVisible()
  await userEvent.setup().click(screen.getByRole('button', { name: camera.cameraStart }))
  expect(acquire).toHaveBeenCalledOnce()
})

it('stops a camera permission result that arrives after session loss', async () => {
  let release!: (stream: MediaStream) => void
  acquire.mockReturnValueOnce(new Promise((resolve) => { release = resolve }))
  mount()
  await userEvent.setup().click(screen.getByRole('button', { name: camera.cameraStart }))
  expect(screen.getByText(camera.cameraWaiting)).toBeVisible()
  act(() => { notifyPageSuspended() })
  const track = new Track(), stream = { getTracks: () => [track], getVideoTracks: () => [track], getAudioTracks: () => [] } as unknown as MediaStream
  await act(async () => { release(stream) })
  expect(track.stop).toHaveBeenCalledOnce()
  expect(screen.queryByLabelText(camera.cameraVideo)).not.toBeInTheDocument()
  expect(createURL).not.toHaveBeenCalled()
})

it('retains existing photos when a new frame would exceed the encoded budget', async () => {
  const large = new Blob(['small fixture'], { type: 'image/jpeg' })
  Object.defineProperty(large, 'size', { configurable: true, value: 32 * 1024 * 1024 })
  encoding.mockImplementation((callback: BlobCallback) => { callback(large) })
  mount(); await openAndCapture(); await openAndCapture()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  await user.click(await screen.findByRole('button', { name: text.take }))
  expect(await screen.findByRole('alert')).toHaveTextContent(text.frameErrors['bytes-limit'])
  expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button')).toHaveLength(2)
  expect(createURL).toHaveBeenCalledTimes(2)
  expect(tracks[2]?.stop).toHaveBeenCalledOnce()
})

it('keeps the ten-photo limit while allowing an explicitly confirmed retake', async () => {
  mount()
  const user = userEvent.setup()
  for (let index = 1; index <= 10; index++) {
    await openAndCapture()
    await waitFor(() => expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button')).toHaveLength(index))
  }
  expect(screen.getByRole('button', { name: camera.cameraStart })).toBeDisabled()
  expect(screen.getByText(text.cameraLimit)).toBeVisible()
  await user.click(screen.getByRole('button', { name: text.retake }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.confirmRetake }))
  await user.click(await screen.findByRole('button', { name: text.take }))
  await waitFor(() => expect(createURL).toHaveBeenCalledTimes(11))
  expect(within(screen.getByRole('list', { name: text.album })).getAllByRole('button')).toHaveLength(10)
  expect(revokeURL).toHaveBeenCalledWith('blob:homebox-test/10')
})

it('retains an unsaved album through canceled unload and requires explicit navigation confirmation', async () => {
  const { router } = mount(); await openAndCapture()
  const event = new Event('beforeunload', { cancelable: true })
  fireEvent(window, event)
  expect(event.defaultPrevented).toBe(true)
  expect(screen.getByRole('img', { name: text.preview })).toBeVisible()
  const user = userEvent.setup()
  await user.click(screen.getByRole('link', { name: 'Next page' }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent(text.leaveHelp)
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.stay }))
  expect(screen.getByRole('img', { name: text.preview })).toBeVisible()
  expect(router.state.location.pathname).toBe('/')
  await user.click(screen.getByRole('link', { name: 'Next page' }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.leave }))
  expect(await screen.findByRole('heading', { name: 'Next page reached' })).toBeVisible()
  expect(revokeURL).toHaveBeenCalledWith('blob:homebox-test/1')
})

it('hides but retains the album during session revalidation and still guards navigation', async () => {
  mount(); await openAndCapture()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Toggle session check' }))
  expect(screen.getByText(text.inactive)).toBeVisible()
  expect(screen.queryByRole('img', { name: text.preview })).not.toBeInTheDocument()
  expect(revokeURL).not.toHaveBeenCalled()
  await user.click(screen.getByRole('link', { name: 'Next page' }))
  await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: text.stay }))
  await user.click(screen.getByRole('button', { name: 'Toggle session check' }))
  expect(screen.getByRole('img', { name: text.preview })).toBeVisible()
  expect(acquire).toHaveBeenCalledOnce()
})

it('stops the camera and hides private diagnostics if the frame helper rejects', async () => {
  vi.spyOn(frames, 'captureVideoFrame').mockRejectedValueOnce(new Error('private device details'))
  mount()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  await user.click(await screen.findByRole('button', { name: text.take }))
  expect(await screen.findByRole('alert')).toHaveTextContent(text.frameErrors.failed)
  expect(screen.queryByText('private device details')).not.toBeInTheDocument()
  expect(tracks[0]?.stop).toHaveBeenCalledOnce()
  expect(screen.getByRole('button', { name: camera.cameraStart })).toBeEnabled()
})

it('maps a timed-out frame to reload guidance without opening the camera again', async () => {
  vi.spyOn(frames, 'captureVideoFrame').mockResolvedValueOnce({ kind: 'timeout' })
  mount()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: camera.cameraStart }))
  await user.click(await screen.findByRole('button', { name: text.take }))
  expect(await screen.findByRole('alert')).toHaveTextContent(text.frameErrors.timeout)
  expect(tracks[0]?.stop).toHaveBeenCalledOnce()
  expect(screen.getByRole('button', { name: camera.cameraStart })).toBeDisabled()
})
