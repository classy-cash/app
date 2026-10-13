<?php

declare(strict_types=1);

namespace App\Tests\Application\CommandHandler\Notification;

use App\Application\Command\Notification\SendSettlementConfirmationEmail;
use App\Application\CommandHandler\Notification\SendSettlementConfirmationEmailHandler;
use App\Entity\ClassCouncil\ClassMembership;
use App\Entity\ClassCouncil\ClassRole;
use App\Entity\ClassCouncil\ClassRoom;
use App\Entity\ClassCouncil\Student;
use App\Entity\ClassCouncil\StudentPayment;
use App\Entity\Payment;
use App\Entity\User;
use App\Repository\ClassCouncil\ClassMembershipRepository;
use App\Repository\ClassCouncil\StudentPaymentRepository;
use App\Repository\PaymentRepository;
use Brick\Money\Money;
use PHPUnit\Framework\Attributes\Group;
use PHPUnit\Framework\MockObject\MockObject;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Mailer\MailerInterface;
use Symfony\Component\Mime\Email;
use Twig\Environment as TwigEnvironment;

#[Group('unit')]
final class SendSettlementConfirmationEmailHandlerTest extends TestCase
{
    private PaymentRepository&MockObject $payments;

    private StudentPaymentRepository&MockObject $studentPayments;

    private ClassMembershipRepository&MockObject $memberships;

    private MailerInterface&MockObject $mailer;

    private TwigEnvironment&MockObject $twig;

    protected function setUp(): void
    {
        $this->payments = $this->createMock(PaymentRepository::class);
        $this->studentPayments = $this->createMock(StudentPaymentRepository::class);
        $this->memberships = $this->createMock(ClassMembershipRepository::class);
        $this->mailer = $this->createMock(MailerInterface::class);
        $this->twig = $this->createMock(TwigEnvironment::class);
    }

    public function testThrowsWhenPaymentDoesNotExist(): void
    {
        $this->payments->method('find')
            ->with('missing')
            ->willReturn(null);
        $this->mailer->expects($this->never())
            ->method('send');

        $this->expectException(\InvalidArgumentException::class);

        $this->handle('missing');
    }

    public function testSendsNothingWhenPayerHasNoEmailAddress(): void
    {
        $payment = $this->payment(new User('', 'Anna Kowalska'));
        $this->studentPayments->expects($this->never())
            ->method('findByPayment');
        $this->mailer->expects($this->never())
            ->method('send');

        $this->handle($payment);
    }

    public function testSendsNothingWhenPaymentSettlesNoStudentPayments(): void
    {
        $payment = $this->payment(new User('parent@example.com', 'Anna Kowalska'));
        $this->studentPayments->method('findByPayment')
            ->willReturn([]);
        $this->memberships->expects($this->never())
            ->method('findOneBy');
        $this->mailer->expects($this->never())
            ->method('send');

        $this->handle($payment);
    }

    public function testSendsNothingWhenClassHasNoTreasurer(): void
    {
        $payment = $this->payment(new User('parent@example.com', 'Anna Kowalska'));
        $classRoom = new ClassRoom('4B');
        $this->studentPayments->method('findByPayment')
            ->willReturn([$this->studentPayment($classRoom, 'Składka', '50.00')]);
        $this->memberships->method('findOneBy')
            ->with([
                'classRoom' => $classRoom,
                'role' => ClassRole::TREASURER,
            ])
            ->willReturn(null);
        $this->twig->expects($this->never())
            ->method('render');
        $this->mailer->expects($this->never())
            ->method('send');

        $this->handle($payment);
    }

    public function testSendsNothingWhenTreasurerHasNoEmailAddress(): void
    {
        $payment = $this->payment(new User('parent@example.com', 'Anna Kowalska'));
        $classRoom = new ClassRoom('4B');
        $this->studentPayments->method('findByPayment')
            ->willReturn([$this->studentPayment($classRoom, 'Składka', '50.00')]);
        $this->memberships->method('findOneBy')
            ->willReturn(new ClassMembership(new User('', 'Skarbnik'), $classRoom, ClassRole::TREASURER));
        $this->twig->expects($this->never())
            ->method('render');
        $this->mailer->expects($this->never())
            ->method('send');

        $this->handle($payment);
    }

    public function testNotifiesTreasurerWithSettledItemsAndMissingAmount(): void
    {
        $parent = new User('parent@example.com', 'Anna Kowalska');
        $payment = $this->payment($parent);
        $classRoom = new ClassRoom('4B');
        $settled = $this->studentPayment($classRoom, 'Składka ogólna', '50.00', paid: true);

        $this->studentPayments->method('findByPayment')
            ->willReturn([$settled]);
        $this->memberships->method('findOneBy')
            ->with([
                'classRoom' => $classRoom,
                'role' => ClassRole::TREASURER,
            ])
            ->willReturn(new ClassMembership(
                new User('treasurer@example.com', 'Skarbnik Test'),
                $classRoom,
                ClassRole::TREASURER
            ));
        $this->studentPayments->expects($this->once())
            ->method('findBy')
            ->with([
                'classRoom' => $classRoom,
                'status' => ['pending', 'partial'],
            ])
            ->willReturn([
                $this->studentPayment($classRoom, 'Wycieczka', '30.50'),
                $this->studentPayment($classRoom, 'Wycieczka', '20.25'),
            ]);

        $this->twig->expects($this->once())
            ->method('render')
            ->with(
                'email/class_council/treasurer_payment_notification.html.twig',
                $this->callback(static fn(array $context): bool => $context['classRoomName'] === '4B'
                    && $context['parentName'] === 'Anna Kowalska'
                    && $context['parentEmail'] === 'parent@example.com'
                    && abs($context['missingAmount'] - 50.75) < 0.0001
                    && count($context['items']) === 1
                    && $context['items'][0]['student'] === 'Jan Kowalski'
                    && $context['items'][0]['label'] === 'Składka ogólna'
                    && $context['items'][0]['status'] === StudentPayment::STATUS_PAID)
            )
            ->willReturn('<p>treasurer notification</p>');

        $this->mailer->expects($this->once())
            ->method('send')
            ->with(
                $this->callback(
                    static fn(Email $email): bool => $email->getTo()[0]
                        ->getAddress() === 'treasurer@example.com'
                && $email->getSubject() === 'Nowa wpłata w klasie - podsumowanie zaległości'
                && $email->getHtmlBody() === '<p>treasurer notification</p>'
                )
            );

        $this->handle($payment);
    }

    private function handle(Payment|string $payment): void
    {
        $handler = new SendSettlementConfirmationEmailHandler(
            $this->payments,
            $this->studentPayments,
            $this->memberships,
            $this->mailer,
            $this->twig,
        );

        $handler(new SendSettlementConfirmationEmail(
            $payment instanceof Payment ? (string) $payment->getId() : $payment
        ));
    }

    private function payment(User $payer): Payment
    {
        $payment = new Payment($payer, Money::of(50, 'PLN'));
        $this->payments->method('find')
            ->with((string) $payment->getId())
            ->willReturn($payment);

        return $payment;
    }

    private function studentPayment(
        ClassRoom $classRoom,
        string $label,
        string $amount,
        bool $paid = false
    ): StudentPayment {
        $studentPayment = new StudentPayment(
            new Student($classRoom, 'Jan', 'Kowalski'),
            $label,
            Money::of($amount, 'PLN'),
        );

        if ($paid) {
            $studentPayment->markPaid();
        }

        return $studentPayment;
    }
}
