package com.wthomson.ctrlfreak

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Rect
import android.util.AttributeSet
import android.view.MotionEvent
import android.view.View
import android.view.ViewConfiguration
import kotlin.math.abs
import kotlin.math.hypot

/**
 * Draws the remote screen (a Bitmap decoded from each JPEG frame) and turns
 * touches into remote input, TeamViewer style:
 *
 *   - tap                  = left click
 *   - double tap           = double click (both clicks sent at the SAME remote
 *                            point, so Windows always registers it even if the
 *                            two taps land a few pixels apart)
 *   - press and hold       = right click
 *   - drag (hold + move)   = left button drag
 *   - two-finger pinch     = zoom (anchored to the pinch point)
 *   - two-finger drag      = pan when zoomed in, mouse wheel scroll otherwise
 *
 * Zoom and pan are computed directly from the two finger positions (span and
 * centroid) rather than a gesture detector, so panning is never swallowed by the
 * pinch recognizer.
 */
class RemoteView @JvmOverloads constructor(
    context: Context, attrs: AttributeSet? = null
) : View(context, attrs) {

    interface Listener {
        fun onLeftClick(x: Int, y: Int)
        fun onRightClick(x: Int, y: Int)
        fun onDragStart(x: Int, y: Int)
        fun onDragMove(x: Int, y: Int)
        fun onDragEnd(x: Int, y: Int)
        fun onScroll(dy: Int)
    }

    var listener: Listener? = null
    var remoteW: Int = 0
    var remoteH: Int = 0

    private var bitmap: Bitmap? = null
    private val dst = Rect()

    // View transform: zoom >= 1, with pan offset from the centered position.
    private var zoom = 1f
    private var panX = 0f
    private var panY = 0f

    // One-finger gesture state.
    private var downVX = 0f
    private var downVY = 0f
    private var lastVX = 0f
    private var lastVY = 0f
    private var moved = false
    private var dragging = false
    private var longPressed = false
    private var multi = false
    private var lastTapTime = 0L
    private var lastTapVX = 0f
    private var lastTapVY = 0f

    // Two-finger gesture state.
    private var prevCx = 0f
    private var prevCy = 0f
    private var prevSpan = 0f
    private var scrollAnchorY = 0f

    private val density = resources.displayMetrics.density
    private val slop = ViewConfiguration.get(context).scaledTouchSlop.toFloat()
    private val doubleSlop = 40f * density
    private val pinchSlop = 14f * density
    private val longPress = Runnable {
        if (!moved && !dragging && !multi) {
            longPressed = true
            toRemote(downVX, downVY)?.let { listener?.onRightClick(it.first, it.second) }
        }
    }

    init {
        setBackgroundColor(Color.BLACK)
        isFocusableInTouchMode = true
    }

    /** Set the current frame. Call on the main thread. */
    fun setFrame(bmp: Bitmap) {
        bitmap = bmp
        if (remoteW == 0) remoteW = bmp.width
        if (remoteH == 0) remoteH = bmp.height
        invalidate()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        val bmp = bitmap ?: return
        val g = geom() ?: return
        dst.set(g[0].toInt(), g[1].toInt(), (g[0] + g[2]).toInt(), (g[1] + g[3]).toInt())
        canvas.drawBitmap(bmp, null, dst, null)
    }

    /** Current image rectangle in view coords: [originX, originY, imgW, imgH]. */
    private fun geom(): FloatArray? {
        val bmp = bitmap ?: return null
        val sw = bmp.width.toFloat(); val sh = bmp.height.toFloat()
        val vw = width.toFloat(); val vh = height.toFloat()
        if (sw <= 0f || sh <= 0f || vw <= 0f || vh <= 0f) return null
        val base = minOf(vw / sw, vh / sh)
        val iw = sw * base * zoom; val ih = sh * base * zoom
        val ox = (vw - iw) / 2f + panX
        val oy = (vh - ih) / 2f + panY
        return floatArrayOf(ox, oy, iw, ih)
    }

    private fun clampPan() {
        val g = geom() ?: return
        val vw = width.toFloat(); val vh = height.toFloat()
        val maxX = maxOf(0f, (g[2] - vw) / 2f)
        val maxY = maxOf(0f, (g[3] - vh) / 2f)
        panX = panX.coerceIn(-maxX, maxX)
        panY = panY.coerceIn(-maxY, maxY)
    }

    private fun toRemote(vx: Float, vy: Float): Pair<Int, Int>? {
        val g = geom() ?: return null
        if (g[2] <= 0f || g[3] <= 0f) return null
        val fx = ((vx - g[0]) / g[2]).coerceIn(0f, 1f)
        val fy = ((vy - g[1]) / g[3]).coerceIn(0f, 1f)
        val rw = if (remoteW > 0) remoteW else 1
        val rh = if (remoteH > 0) remoteH else 1
        return Pair((fx * rw).toInt(), (fy * rh).toInt())
    }

    private fun cx(e: MotionEvent) = (e.getX(0) + e.getX(1)) / 2f
    private fun cy(e: MotionEvent) = (e.getY(0) + e.getY(1)) / 2f
    private fun span(e: MotionEvent): Float {
        if (e.pointerCount < 2) return 0f
        val dx = (e.getX(0) - e.getX(1)).toDouble()
        val dy = (e.getY(0) - e.getY(1)).toDouble()
        return hypot(dx, dy).toFloat()
    }

    /** Change zoom by factor, keeping the point (fx,fy) fixed under the fingers. */
    private fun zoomAround(fx: Float, fy: Float, factor: Float) {
        val bmp = bitmap ?: return
        val sw = bmp.width.toFloat(); val sh = bmp.height.toFloat()
        val vw = width.toFloat(); val vh = height.toFloat()
        if (sw <= 0f || sh <= 0f || vw <= 0f || vh <= 0f) return
        val base = minOf(vw / sw, vh / sh)
        val newZoom = (zoom * factor).coerceIn(1f, 5f)
        if (newZoom == zoom) return
        val real = newZoom / zoom
        val iw0 = sw * base * zoom; val ih0 = sh * base * zoom
        val ox0 = (vw - iw0) / 2f + panX; val oy0 = (vh - ih0) / 2f + panY
        val nox = fx - real * (fx - ox0); val noy = fy - real * (fy - oy0)
        zoom = newZoom
        val iw1 = sw * base * zoom; val ih1 = sh * base * zoom
        panX = nox - (vw - iw1) / 2f
        panY = noy - (vh - ih1) / 2f
    }

    override fun onTouchEvent(event: MotionEvent): Boolean {
        val l = listener ?: return false
        when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                downVX = event.x; downVY = event.y; lastVX = event.x; lastVY = event.y
                moved = false; dragging = false; longPressed = false; multi = false
                postDelayed(longPress, 500)
            }
            MotionEvent.ACTION_POINTER_DOWN -> {
                removeCallbacks(longPress)
                if (dragging) { toRemote(lastVX, lastVY)?.let { l.onDragEnd(it.first, it.second) }; dragging = false }
                multi = true
                if (event.pointerCount >= 2) {
                    prevCx = cx(event); prevCy = cy(event); prevSpan = span(event); scrollAnchorY = prevCy
                }
            }
            MotionEvent.ACTION_MOVE -> {
                if (multi) {
                    if (event.pointerCount >= 2) {
                        val ncx = cx(event); val ncy = cy(event); val nspan = span(event)
                        val pinching = prevSpan > 0f && abs(nspan - prevSpan) > pinchSlop
                        if (zoom > 1f || pinching) {
                            if (prevSpan > 0f && nspan > 0f) zoomAround(ncx, ncy, nspan / prevSpan)
                            panX += (ncx - prevCx); panY += (ncy - prevCy)
                            clampPan(); invalidate()
                        } else if (abs(ncy - scrollAnchorY) > 24f) {
                            l.onScroll(if (ncy > scrollAnchorY) -1 else 1); scrollAnchorY = ncy
                        }
                        prevCx = ncx; prevCy = ncy; prevSpan = nspan
                    }
                } else {
                    val dx = event.x - downVX; val dy = event.y - downVY
                    if (!dragging && (abs(dx) > slop || abs(dy) > slop)) {
                        moved = true; removeCallbacks(longPress)
                        if (!longPressed) {
                            dragging = true
                            toRemote(downVX, downVY)?.let { l.onDragStart(it.first, it.second) }
                        }
                    }
                    if (dragging) {
                        lastVX = event.x; lastVY = event.y
                        toRemote(event.x, event.y)?.let { l.onDragMove(it.first, it.second) }
                    }
                }
            }
            MotionEvent.ACTION_POINTER_UP -> {
                // If we drop back to one finger, re-seed for a possible continued
                // single-finger gesture but stay in multi mode until full release,
                // so the leftover finger does not fire a stray click.
            }
            MotionEvent.ACTION_UP -> {
                removeCallbacks(longPress)
                when {
                    multi -> multi = false
                    dragging -> { toRemote(event.x, event.y)?.let { l.onDragEnd(it.first, it.second) }; dragging = false }
                    longPressed -> longPressed = false
                    else -> {
                        val now = System.currentTimeMillis()
                        val near = hypot((downVX - lastTapVX).toDouble(), (downVY - lastTapVY).toDouble()) < doubleSlop
                        if (now - lastTapTime < 320L && near) {
                            toRemote(lastTapVX, lastTapVY)?.let { l.onLeftClick(it.first, it.second) }
                            lastTapTime = 0L
                        } else {
                            toRemote(downVX, downVY)?.let { l.onLeftClick(it.first, it.second) }
                            lastTapTime = now; lastTapVX = downVX; lastTapVY = downVY
                        }
                    }
                }
            }
            MotionEvent.ACTION_CANCEL -> {
                removeCallbacks(longPress)
                if (dragging) toRemote(lastVX, lastVY)?.let { l.onDragEnd(it.first, it.second) }
                dragging = false; multi = false; longPressed = false
            }
        }
        return true
    }
}
