<?php

declare(strict_types=1);

namespace App\Tests\Infrastructure\Symfony\EventSubscriber;

use App\Entity\ClassCouncil\ClassRoom;
use App\Entity\ClassCouncil\Student;
use App\Entity\ClassCouncil\StudentPayment;
use App\Entity\Payment;
use App\Entity\User;
use App\Infrastructure\Symfony\EventSubscriber\StudentPaymentSettlementSubscriber;
use App\Repository\ClassCouncil\StudentPaymentRepository;
use Brick\Money\Money;
use Doctrine\ORM\EntityManagerInterface;
use PHPUnit\Framework\Attributes\Group;
use PHPUnit\Framework\MockObject\MockObject;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Mailer\MailerInterface;
use Symfony\Component\Workflow\Event\EnteredEvent;
use Symfony\Component\Workflow\Marking;
use Symfony\Component\Workflow\WorkflowInterface;
use Twig\Environment as TwigEnvironment;

#[Group('unit')]
final class StudentPaymentSettlementSubscriberTest extends TestCase
{
    private StudentPaymentRepository&MockObject $studentPayments;

    private EntityManagerInterface&MockObject $em;

    private WorkflowInterface&MockObject $stateMachine;

    private MailerInterface&MockObject $mailer;

    private TwigEnvironment&MockObject $twig;

    protected function setUp(): void
    {
        $this->studentPayments = $this->createMock(StudentPaymentRepository::class);
        $this->em = $this->createMock(EntityManagerInterface::class);
        $this->stateMachine = $this->createMock(WorkflowInterface::class);
        $this->mailer = $this->createMock(MailerInterface::class);
        $this->twig = $this->createMock(TwigEnvironment::class);
    }

    public function testIgnoresSubjectsThatAreNotPayments(): void
    {
        $this->studentPayments->expects($this->never())
            ->method('findByPayment');
        $this->em->expects($this->never())
            ->method('flush');
        $this->mailer->expects($this->never())
            ->method('send');

        $this->subscriber()
            ->onPaymentEnteredPaid($this->event(new \stdClass()));
    }

    public function testDoesNothingWhenPaymentHasNoStudentPayments(): void
    {
        $payment = $this->payment();
        $this->studentPayments->method('findByPayment')
            ->with($payment)
            ->willReturn([]);
        $this->stateMachine->expects($this->never())
            ->method('can');
        $this->em->expects($this->never())
            ->method('flush');
        $this->mailer->expects($this->never())
            ->method('send');

        $this->subscriber()
            ->onPaymentEnteredPaid($this->event($payment));
    }

    public function testSettlesStudentPaymentsThroughTheWorkflow(): void
    {
        $payment = $this->payment();
        $studentPayment = $this->studentPayment();
        $this->studentPayments->method('findByPayment')
            ->willReturn([$studentPayment]);
        $this->stateMachine->method('can')
            ->with($studentPayment, 'settle')
            ->willReturn(true);
        $this->stateMachine->expects($this->once())
            ->method('apply')
            ->with($studentPayment, 'settle');
        $this->em->expects($this->once())
            ->method('flush');

        $this->subscriber()
            ->onPaymentEnteredPaid($this->event($payment));

        // The workflow is the one moving the status; the fallback must not interfere
        self::assertSame(StudentPayment::STATUS_PENDING, $studentPayment->getStatus());
    }

    public function testMarksStudentPaymentPaidDirectlyWhenWorkflowCannotSettle(): void
    {
        $payment = $this->payment();
        $studentPayment = $this->studentPayment();
        $this->studentPayments->method('findByPayment')
            ->willReturn([$studentPayment]);
        $this->stateMachine->method('can')
            ->with($studentPayment, 'settle')
            ->willReturn(false);
        $this->stateMachine->expects($this->never())
            ->method('apply');
        $this->em->expects($this->once())
            ->method('flush');

        $this->subscriber()
            ->onPaymentEnteredPaid($this->event($payment));

        self::assertSame(StudentPayment::STATUS_PAID, $studentPayment->getStatus());
        self::assertNotNull($studentPayment->getPaidAt());
    }

    public function testSettlesButDoesNotSendConfirmationWhenPayerHasNoEmail(): void
    {
        $payment = $this->payment(new User('', 'Anna Kowalska'));
        $this->studentPayments->method('findByPayment')
            ->willReturn([$this->studentPayment()]);
        $this->studentPayments->expects($this->never())
            ->method('getCountsForClassAndLabel');
        $this->stateMachine->method('can')
            ->willReturn(true);
        $this->em->expects($this->once())
            ->method('flush');
        $this->twig->expects($this->never())
            ->method('render');
        $this->mailer->expects($this->never())
            ->method('send');

        $this->subscriber()
            ->onPaymentEnteredPaid($this->event($payment));
    }

    private function subscriber(): StudentPaymentSettlementSubscriber
    {
        return new StudentPaymentSettlementSubscriber(
            $this->studentPayments,
            $this->em,
            $this->stateMachine,
            $this->mailer,
            $this->twig,
        );
    }

    /**
     * @return EnteredEvent<object>
     */
    private function event(object $subject): EnteredEvent
    {
        return new EnteredEvent($subject, new Marking([
            'paid' => 1,
        ]));
    }

    private function payment(?User $payer = null): Payment
    {
        return new Payment($payer ?? new User('', 'Anna Kowalska'), Money::of(50, 'PLN'));
    }

    private function studentPayment(): StudentPayment
    {
        return new StudentPayment(
            new Student(new ClassRoom('4B'), 'Jan', 'Kowalski'),
            'Składka ogólna',
            Money::of(50, 'PLN'),
        );
    }
}
