package com.wthomson.ctrlfreak

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Rect
import android.util.AttributeSet
import android.view.MotionEvent
import android.view.View
import kotlin.math.abs

/**
 * Draws the remote screen (a Bitmap decoded from each JPEG frame), fit to the
 * view, and translates touches into remote-screen pixel coordinates.
 *
 * Gestures: one finger = move / drag / click (down-move-up like the web client);
 * two fingers dragging vertically = scroll wheel.
 */
class RemoteView @JvmOverloads constructor(
    context: Context, attrs: AttributeSet? = null
) : View(context, attrs) {

    interface Listener {
        fun onDown(x: Int, y: Int)
        fun onMove(x: Int, y: Int)
        fun onUp(x: Int, y: Int)
        fun onScroll(dy: Int)
    }

    var listener: Listener? = null
    var remoteW: Int = 0
    var remoteH: Int = 0

    private var bitmap: Bitmap? = null
    private val dst = Rect()
    private var twoFinger = false
    private var lastScrollY = 0f

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
        computeDst(bmp.width, bmp.height)
        canvas.drawBitmap(bmp, null, dst, null)
    }

    /** Fit-center the source into the view, storing the destination rect. */
    private fun computeDst(srcW: Int, srcH: Int) {
        if (srcW == 0 || srcH == 0) return
        val vw = width.toFloat()
        val vh = height.toFloat()
        val scale = minOf(vw / srcW, vh / srcH)
        val dw = (srcW * scale).toInt()
        val dh = (srcH * scale).toInt()
        val left = ((vw - dw) / 2f).toInt()
        val top = ((vh - dh) / 2f).toInt()
        dst.set(left, top, left + dw, top + dh)
    }

    private fun toRemote(vx: Float, vy: Float): Pair<Int, Int>? {
        if (dst.width() == 0 || dst.height() == 0) return null
        val fx = ((vx - dst.left) / dst.width()).coerceIn(0f, 1f)
        val fy = ((vy - dst.top) / dst.height()).coerceIn(0f, 1f)
        val rw = if (remoteW > 0) remoteW else 1
        val rh = if (remoteH > 0) remoteH else 1
        return Pair((fx * rw).toInt(), (fy * rh).toInt())
    }

    override fun onTouchEvent(event: MotionEvent): Boolean {
        val l = listener ?: return false
        when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                twoFinger = false
                toRemote(event.x, event.y)?.let { l.onDown(it.first, it.second) }
            }
            MotionEvent.ACTION_POINTER_DOWN -> {
                if (event.pointerCount >= 2) {
                    twoFinger = true
                    lastScrollY = event.getY(0)
                    // Cancel the in-progress left drag so a two-finger gesture is a
                    // clean scroll, not a drag.
                    toRemote(event.x, event.y)?.let { l.onUp(it.first, it.second) }
                }
            }
            MotionEvent.ACTION_MOVE -> {
                if (twoFinger && event.pointerCount >= 2) {
                    val y = event.getY(0)
                    val d = y - lastScrollY
                    if (abs(d) > 24) {
                        l.onScroll(if (d > 0) -1 else 1)
                        lastScrollY = y
                    }
                } else if (!twoFinger) {
                    toRemote(event.x, event.y)?.let { l.onMove(it.first, it.second) }
                }
            }
            MotionEvent.ACTION_UP, MotionEvent.ACTION_CANCEL -> {
                if (!twoFinger) toRemote(event.x, event.y)?.let { l.onUp(it.first, it.second) }
                twoFinger = false
            }
        }
        return true
    }
}
